package main_test

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"
	"github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/ghttp"
)

var _ = Describe("Launcher", func() {
	var extractDir string
	var appDir string
	var launcherCmd *exec.Cmd
	var session *gexec.Session
	var startCommand string

	BeforeEach(func() {
		Expect(os.Setenv("CALLERENV", "some-value")).To(Succeed())

		if runtime.GOOS == "windows" {
			startCommand = "cmd /C set && echo PWD=%cd% && echo running app"
		} else {
			startCommand = "env; echo running app"
		}

		var err error
		extractDir, err = ioutil.TempDir("", "vcap")
		Expect(err).NotTo(HaveOccurred())

		appDir = filepath.Join(extractDir, "app")
		err = os.MkdirAll(appDir, 0755)
		Expect(err).NotTo(HaveOccurred())

		launcherCmd = &exec.Cmd{
			Path: launcher,
			Dir:  extractDir,
			Env: append(
				os.Environ(),
				"TEST_CREDENTIAL_FILTER_WHITELIST=CALLERENV,DEPS_DIR,VCAP_APPLICATION,VCAP_SERVICES,A,B,C,INSTANCE_GUID,INSTANCE_INDEX,PORT",
				"PORT=8080",
				"INSTANCE_GUID=some-instance-guid",
				"INSTANCE_INDEX=123",
				`VCAP_APPLICATION={"foo":1}`,
			),
		}
	})

	AfterEach(func() {
		err := os.RemoveAll(extractDir)
		Expect(err).NotTo(HaveOccurred())
	})

	JustBeforeEach(func() {
		var err error
		session, err = gexec.Start(launcherCmd, GinkgoWriter, GinkgoWriter)
		Expect(err).NotTo(HaveOccurred())
	})

	var ItExecutesTheCommandWithTheRightEnvironment = func() {
		It("executes with the environment of the caller", func() {
			Eventually(session).Should(gexec.Exit(0))
			Expect(string(session.Out.Contents())).To(ContainSubstring("CALLERENV=some-value"))
		})

		It("executes the start command with $HOME as the given dir", func() {
			Eventually(session).Should(gexec.Exit(0))
			Expect(string(session.Out.Contents())).To(ContainSubstring("HOME=" + appDir))
		})

		It("changes to the app directory when running", func() {
			Eventually(session).Should(gexec.Exit(0))
			Expect(string(session.Out.Contents())).To(ContainSubstring("PWD=" + appDir))
		})

		It("executes the start command with $TMPDIR as the extract directory + /tmp", func() {
			absDir, err := filepath.Abs(filepath.Join(appDir, "..", "tmp"))
			Expect(err).NotTo(HaveOccurred())

			Eventually(session).Should(gexec.Exit(0))
			Expect(string(session.Out.Contents())).To(ContainSubstring("TMPDIR=" + absDir))
		})

		It("executes the start command with $DEPS_DIR as the extract directory + /deps", func() {
			absDir, err := filepath.Abs(filepath.Join(appDir, "..", "deps"))
			Expect(err).NotTo(HaveOccurred())

			Eventually(session).Should(gexec.Exit(0))
			Expect(string(session.Out.Contents())).To(ContainSubstring("DEPS_DIR=" + absDir))
		})

		It("munges VCAP_APPLICATION appropriately", func() {
			Eventually(session).Should(gexec.Exit(0))

			vcapAppPattern := regexp.MustCompile("VCAP_APPLICATION=(.*)")
			vcapApplicationBytes := vcapAppPattern.FindSubmatch(session.Out.Contents())[1]

			vcapApplication := map[string]interface{}{}
			err := json.Unmarshal(vcapApplicationBytes, &vcapApplication)
			Expect(err).NotTo(HaveOccurred())

			Expect(vcapApplication["host"]).To(Equal("0.0.0.0"))
			Expect(vcapApplication["port"]).To(Equal(float64(8080)))
			Expect(vcapApplication["instance_index"]).To(Equal(float64(123)))
			Expect(vcapApplication["instance_id"]).To(Equal("some-instance-guid"))
			Expect(vcapApplication["foo"]).To(Equal(float64(1)))
		})

		Context("when the given dir has .profile.d with scripts in it", func() {
			BeforeEach(func() {
				if runtime.GOOS == "windows" {
					Skip(".profile.d not supported on Windows")
				}

				var err error

				profileDir := filepath.Join(appDir, ".profile.d")

				err = os.MkdirAll(profileDir, 0755)
				Expect(err).NotTo(HaveOccurred())

				err = ioutil.WriteFile(filepath.Join(profileDir, "a.sh"), []byte("echo sourcing a\nexport A=1\n"), 0644)
				Expect(err).NotTo(HaveOccurred())

				err = ioutil.WriteFile(filepath.Join(profileDir, "b.sh"), []byte("echo sourcing b\nexport B=1\n"), 0644)
				Expect(err).NotTo(HaveOccurred())

				err = ioutil.WriteFile(filepath.Join(appDir, ".profile"), []byte("echo sourcing .profile\nexport C=$A$B\n"), 0644)
				Expect(err).NotTo(HaveOccurred())

			})

			It("sources them before sourcing .profile and before executing", func() {
				Eventually(session).Should(gexec.Exit(0))
				Eventually(session).Should(gbytes.Say("sourcing a"))
				Eventually(session).Should(gbytes.Say("sourcing b"))
				Eventually(session).Should(gbytes.Say("sourcing .profile"))
				Eventually(session).Should(gbytes.Say("A=1"))
				Eventually(session).Should(gbytes.Say("B=1"))
				Eventually(session).Should(gbytes.Say("C=11"))
				Eventually(session).Should(gbytes.Say("running app"))
			})
		})

		Context("when the given dir does not have .profile.d", func() {
			It("does not report errors about missing .profile.d", func() {
				Eventually(session).Should(gexec.Exit(0))
				Expect(string(session.Err.Contents())).To(BeEmpty())
			})
		})

		Context("when the given dir has an empty .profile.d", func() {
			BeforeEach(func() {
				if runtime.GOOS == "windows" {
					Skip(".profile.d not supported on Windows")
				}
				Expect(os.MkdirAll(filepath.Join(appDir, ".profile.d"), 0755)).To(Succeed())
			})

			It("does not report errors about missing .profile.d", func() {
				Eventually(session).Should(gexec.Exit(0))
				Expect(string(session.Err.Contents())).To(BeEmpty())
			})
		})
	}

	Context("the app executable is in vcap/app", func() {
		BeforeEach(func() {
			copyExe := func(dstDir, src string) error {
				in, err := os.Open(src)
				if err != nil {
					return err
				}
				defer in.Close()

				exeName := filepath.Base(src)
				dst := filepath.Join(dstDir, exeName)
				out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE, 0755)
				if err != nil {
					return err
				}
				defer out.Close()
				_, err = io.Copy(out, in)
				cerr := out.Close()
				if err != nil {
					return err
				}
				return cerr
			}

			Expect(copyExe(appDir, hello)).To(Succeed())

			launcherCmd.Args = []string{
				"launcher",
				appDir,
				"./hello",
				`{ "start_command": "echo should not run this" }`,
			}
		})

		It("finds the app executable", func() {
			Eventually(session).Should(gexec.Exit(0))
			Expect(string(session.Out.Contents())).To(ContainSubstring("app is running"))
		})
	})

	Context("when a start command is given", func() {
		BeforeEach(func() {
			launcherCmd.Args = []string{
				"launcher",
				appDir,
				startCommand,
				`{ "start_command": "echo should not run this" }`,
			}
		})

		ItExecutesTheCommandWithTheRightEnvironment()
	})

	Describe("interpolation of credhub-ref in VCAP_SERVICES", func() {
		var (
			server         *ghttp.Server
			sslCertDir     string
			fixturesSslDir string
		)

		VerifyClientCerts := func() http.HandlerFunc {
			return func(w http.ResponseWriter, req *http.Request) {
				tlsConnectionState := req.TLS
				Expect(tlsConnectionState).NotTo(BeNil())
				Expect(tlsConnectionState.PeerCertificates).NotTo(BeEmpty())
				Expect(tlsConnectionState.PeerCertificates[0].Subject.CommonName).To(Equal("example.com"))
			}
		}

		BeforeEach(func() {
			sslCertDir = os.Getenv("SSL_CERT_DIR")
			fixturesSslDir, err := filepath.Abs("./fixtures/")
			Expect(err).NotTo(HaveOccurred())
			os.Setenv("SSL_CERT_DIR", fixturesSslDir)

			server = ghttp.NewUnstartedServer()

			cert, err := tls.LoadX509KeyPair("./fixtures/server-tls-cert.pem", "./fixtures/server-tls-key.pem")
			Expect(err).NotTo(HaveOccurred())
			server.HTTPTestServer.TLS = &tls.Config{
				ClientAuth:   tls.RequireAndVerifyClientCert,
				Certificates: []tls.Certificate{cert},
			}
			server.HTTPTestServer.StartTLS()

			launcherCmd.Env = append(launcherCmd.Env, fmt.Sprintf("CF_INSTANCE_CERT=%s/client-tls-cert.pem", fixturesSslDir))
			launcherCmd.Env = append(launcherCmd.Env, fmt.Sprintf("CF_INSTANCE_KEY=%s/client-tls-key.pem", fixturesSslDir))
			launcherCmd.Env = append(launcherCmd.Env, fmt.Sprintf("SSL_CERT_DIR=%s", fixturesSslDir))
		})

		AfterEach(func() {
			server.Close()
			os.Setenv("SSL_CERT_DIR", sslCertDir)
		})

		Context("when VCAP_SERVICES contains credhub refs", func() {
			var vcapServicesValue string
			BeforeEach(func() {
				vcapServicesValue = `{"my-server":[{"credentials":{"credhub-ref":"(//my-server/creds)"}}]}`
				launcherCmd.Env = append(launcherCmd.Env, fmt.Sprintf("VCAP_SERVICES=%s", vcapServicesValue))
			})

			Context("when the credhub location is passed to the launcher", func() {
				BeforeEach(func() {
					encodedCredhubLocation := base64.StdEncoding.EncodeToString([]byte(`{ "credhub_uri": "` + server.URL() + `"}`))
					launcherCmd.Args = []string{
						"launcher",
						appDir,
						startCommand,
						"",
						encodedCredhubLocation,
					}
				})

				Context("when credhub successfully interpolates", func() {
					BeforeEach(func() {
						server.AppendHandlers(
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("POST", "/api/v1/interpolate"),
								ghttp.VerifyBody([]byte(vcapServicesValue)),
								VerifyClientCerts(),
								ghttp.RespondWith(http.StatusOK, "INTERPOLATED_JSON"),
							))
					})

					It("updates VCAP_SERVICES with the interpolated content", func() {
						Eventually(session).Should(gexec.Exit(0))
						Eventually(session.Out).Should(gbytes.Say("VCAP_SERVICES=INTERPOLATED_JSON"))
					})
				})

				Context("when credhub fails", func() {
					BeforeEach(func() {
						server.AppendHandlers(
							ghttp.CombineHandlers(
								ghttp.VerifyRequest("POST", "/api/v1/interpolate"),
								ghttp.VerifyBody([]byte(vcapServicesValue)),
								ghttp.RespondWith(http.StatusInternalServerError, "{}"),
							))
					})

					It("prints an error message", func() {
						Eventually(session).Should(gexec.Exit(5))
						Eventually(session.Err).Should(gbytes.Say("Unable to interpolate credhub references"))
					})
				})

				Context("when the instance cert and key are invalid", func() {
					BeforeEach(func() {
						launcherCmd.Env = append(launcherCmd.Env, fmt.Sprintf("CF_INSTANCE_CERT=%s/client-tls-key.pem", fixturesSslDir))
						launcherCmd.Env = append(launcherCmd.Env, fmt.Sprintf("CF_INSTANCE_KEY=%s/client-tls-cert.pem", fixturesSslDir))
					})

					It("prints an error message", func() {
						Eventually(session).Should(gexec.Exit(4))
						Eventually(session.Err).Should(gbytes.Say("Unable to set up credhub client"))
					})
				})

				Context("when the instance cert and key aren't set", func() {
					BeforeEach(func() {
						newEnv := []string{}
						for _, env := range launcherCmd.Env {
							if !(strings.HasPrefix(env, "CF_INSTANCE_CERT") || strings.HasPrefix(env, "CF_INSTANCE_KEY")) {
								newEnv = append(newEnv, env)
							}
						}
						launcherCmd.Env = newEnv
					})

					It("prints an error message", func() {
						Eventually(session).Should(gexec.Exit(6))
						Eventually(session.Err).Should(gbytes.Say("Missing CF_INSTANCE_CERT and/or CF_INSTANCE_KEY"))
					})
				})

			})
			Context("when the credhub location is not passed to the launcher", func() {
				BeforeEach(func() {
					encodedCredhubLocation := base64.StdEncoding.EncodeToString([]byte(`{}`))
					launcherCmd.Args = []string{
						"launcher",
						appDir,
						startCommand,
						"",
						encodedCredhubLocation,
					}
				})

				It("does not attempt to do any credhub interpolation", func() {
					Eventually(session).Should(gexec.Exit(0))
					Eventually(string(session.Out.Contents())).Should(ContainSubstring(fmt.Sprintf(fmt.Sprintf("VCAP_SERVICES=%s", vcapServicesValue))))
				})
			})

			Context("when a 4th argument with invalid Base64 is passed to the launcher", func() {
				BeforeEach(func() {
					encodedCredhubLocation := "!!!" + base64.StdEncoding.EncodeToString([]byte(`{ "credhub_uri": "`+server.URL()+`"}`))
					launcherCmd.Args = []string{
						"launcher",
						appDir,
						startCommand,
						"",
						encodedCredhubLocation,
					}
				})
				It("prints an error message", func() {
					Eventually(session).Should(gexec.Exit(3))
					Eventually(session.Err).Should(gbytes.Say("Invalid platform options"))
				})
			})

			Context("when a 4th argument with invalid JSON is passed to the launcher", func() {
				BeforeEach(func() {
					encodedCredhubLocation := base64.StdEncoding.EncodeToString([]byte(`{"credhub_uri":"missing quote and brace`))
					launcherCmd.Args = []string{
						"launcher",
						appDir,
						startCommand,
						"",
						encodedCredhubLocation,
					}
				})
				It("prints an error message", func() {
					Eventually(session).Should(gexec.Exit(3))
					Eventually(session.Err).Should(gbytes.Say("Invalid platform options"))
				})
			})
		})
	})

	var ItPrintsMissingStartCommandInformation = func() {
		It("fails and reports no start command", func() {
			Eventually(session).Should(gexec.Exit(1))
			Eventually(session.Err).Should(gbytes.Say("launcher: no start command specified or detected in droplet"))
		})
	}

	Context("when no start command is given", func() {
		BeforeEach(func() {
			launcherCmd.Args = []string{
				"launcher",
				appDir,
				"",
				"",
			}
		})

		Context("when the app package does not contain staging_info.yml", func() {
			ItPrintsMissingStartCommandInformation()
		})

		Context("when the app package has a staging_info.yml", func() {

			Context("when it is missing a start command", func() {
				BeforeEach(func() {
					writeStagingInfo(extractDir, "detected_buildpack: Ruby")
				})

				ItPrintsMissingStartCommandInformation()
			})

			Context("when it contains a start command", func() {
				BeforeEach(func() {
					writeStagingInfo(extractDir, "detected_buildpack: Ruby\nstart_command: "+startCommand)
				})

				ItExecutesTheCommandWithTheRightEnvironment()
			})

			Context("when it references unresolvable types in non-essential fields", func() {
				BeforeEach(func() {
					writeStagingInfo(
						extractDir,
						"---\nbuildpack_path: !ruby/object:Pathname\n  path: /tmp/buildpacks/null-buildpack\ndetected_buildpack: \nstart_command: "+startCommand+"\n",
					)
				})

				ItExecutesTheCommandWithTheRightEnvironment()
			})

			Context("when it is not valid YAML", func() {
				BeforeEach(func() {
					writeStagingInfo(extractDir, "start_command: &ruby/object:Pathname")
				})

				It("prints an error message", func() {
					Eventually(session).Should(gexec.Exit(1))
					Eventually(session.Err).Should(gbytes.Say("Invalid staging info"))
				})
			})

		})

	})

	Context("when arguments are missing", func() {
		BeforeEach(func() {
			launcherCmd.Args = []string{
				"launcher",
				appDir,
				"env",
			}
		})

		It("fails with an indication that too few arguments were passed", func() {
			Eventually(session).Should(gexec.Exit(1))
			Eventually(session.Err).Should(gbytes.Say("launcher: received only 2 arguments\n"))
			Eventually(session.Err).Should(gbytes.Say("Usage: launcher <app-directory> <start-command> <metadata> \\[<platform-options>\\]"))
		})
	})
})

func writeStagingInfo(extractDir, stagingInfo string) {
	err := ioutil.WriteFile(filepath.Join(extractDir, "staging_info.yml"), []byte(stagingInfo), 0644)
	Expect(err).NotTo(HaveOccurred())
}
