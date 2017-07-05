package autoscaler

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/cloudfoundry-incubator/cf-test-helpers/cf"
	"github.com/cloudfoundry-incubator/cf-test-helpers/generator"
	"github.com/cloudfoundry/cf-smoke-tests/smoke"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gexec"
)

var _ = Describe("Autoscaler:", func() {
	var testConfig = smoke.GetConfig()
	var appName string
	var appUrl string
	var expectedNullResponse string

	BeforeEach(func() {
		appName = testConfig.RuntimeApp
		if appName == "" {
			appName = generator.PrefixedRandomName("SMOKES", "APP")
		}

		appUrl = "https://" + appName + "." + testConfig.AppsDomain

		Eventually(func() error {
			var err error
			expectedNullResponse, err = getBodySkipSSL(testConfig.SkipSSLValidation, appUrl)
			return err
		}, CF_TIMEOUT_IN_SECONDS).Should(BeNil())
	})

	AfterEach(func() {
		smoke.AppReport(appName, CF_TIMEOUT_IN_SECONDS)
		if testConfig.Cleanup {
			Expect(cf.Cf("delete", appName, "-f", "-r").Wait(CF_TIMEOUT_IN_SECONDS)).To(Exit(0))
		}
	})

	Context("linux apps", func() {
		It("can be pushed, scaled and deleted", func() {
			Expect(cf.Cf("push", appName, "-p", SIMPLE_RUBY_APP_BITS_PATH, "-d", testConfig.AppsDomain, "--no-start").Wait(CF_PUSH_TIMEOUT_IN_SECONDS)).To(Exit(0))
			smoke.SetBackend(appName)
			Expect(cf.Cf("start", appName).Wait(CF_PUSH_TIMEOUT_IN_SECONDS)).To(Exit(0))

			runPushTests(appName, appUrl, expectedNullResponse, testConfig)
		})
	})

	Context("windows apps", func() {
		It("can be pushed, scaled and deleted", func() {
			smoke.SkipIfWindows(testConfig)

			Expect(cf.Cf("push", appName, "-p", SIMPLE_DOTNET_APP_BITS_PATH, "-d", testConfig.AppsDomain, "-s", "windows2012R2", "-b", "hwc_buildpack", "--no-start").Wait(CF_PUSH_TIMEOUT_IN_SECONDS)).To(Exit(0))
			smoke.EnableDiego(appName)
			Expect(cf.Cf("start", appName).Wait(CF_PUSH_TIMEOUT_IN_SECONDS)).To(Exit(0))

			runPushTests(appName, appUrl, expectedNullResponse, testConfig)
		})
	})
})

func runPushTests(appName, appUrl, expectedNullResponse string, testConfig *smoke.Config) {
	Eventually(func() (string, error) {
		return getBodySkipSSL(testConfig.SkipSSLValidation, appUrl)
	}, CF_TIMEOUT_IN_SECONDS).Should(ContainSubstring("It just needed to be restarted!"))

	instances := 2
	maxAttempts := 30

	ExpectAppToScale(appName, instances)

	ExpectAllAppInstancesToStart(appName, instances, maxAttempts)

	ExpectAllAppInstancesToBeReachable(appUrl, instances, maxAttempts)

	if testConfig.Cleanup {
		Expect(cf.Cf("delete", appName, "-f", "-r").Wait(CF_TIMEOUT_IN_SECONDS)).To(Exit(0))

		Eventually(func() (string, error) {
			return getBodySkipSSL(testConfig.SkipSSLValidation, appUrl)
		}, CF_TIMEOUT_IN_SECONDS).Should(ContainSubstring(string(expectedNullResponse)))
	}
}

func ExpectAppToScale(appName string, instances int) {
	Expect(cf.Cf("scale", appName, "-i", strconv.Itoa(instances)).Wait(CF_SCALE_TIMEOUT_IN_SECONDS)).To(Exit(0))
}

// Gets app status (up to maxAttempts) until all instances are up
func ExpectAllAppInstancesToStart(appName string, instances int, maxAttempts int) {
	var found bool
	expectedOutput := regexp.MustCompile(fmt.Sprintf(`instances:\s+%d/%d`, instances, instances))

	outputMatchers := make([]*regexp.Regexp, instances)
	for i := 0; i < instances; i++ {
		outputMatchers[i] = regexp.MustCompile(fmt.Sprintf(`#%d\s+running`, i))
	}

	for i := 0; i < maxAttempts; i++ {
		session := cf.Cf("app", appName)
		Expect(session.Wait(CF_APP_STATUS_TIMEOUT_IN_SECONDS)).To(Exit(0))

		output := string(session.Out.Contents())
		found = expectedOutput.MatchString(output)

		if found {
			for _, matcher := range outputMatchers {
				matches := matcher.FindStringSubmatch(output)
				if matches == nil {
					found = false
					break
				}
			}
		}

		if found {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	Expect(found).To(BeTrue(), fmt.Sprintf("Wanted to see '%s' (all instances running) in %d attempts, but didn't", expectedOutput, maxAttempts))
}

// Curls the appUrl (up to maxAttempts) until all instances have been seen
func ExpectAllAppInstancesToBeReachable(appUrl string, instances int, maxAttempts int) {
	matcher := regexp.MustCompile(`instance[ _]index["]{0,1}:[ ]{0,1}(\d+)`)

	branchesSeen := make([]bool, instances)
	var sawAll bool
	var testConfig = smoke.GetConfig()
	for i := 0; i < maxAttempts; i++ {
		var output string
		Eventually(func() error {
			var err error
			output, err = getBodySkipSSL(testConfig.SkipSSLValidation, appUrl)
			return err
		}, CF_TIMEOUT_IN_SECONDS).Should(BeNil())

		matches := matcher.FindStringSubmatch(output)
		if matches == nil {
			Fail("Expected app curl output to include an instance_index; got " + output)
		}
		indexString := matches[1]
		index, err := strconv.Atoi(indexString)
		if err != nil {
			Fail("Failed to parse instance index value " + indexString)
		}
		branchesSeen[index] = true

		if allTrue(branchesSeen) {
			sawAll = true
			break
		}
	}

	Expect(sawAll).To(BeTrue(), fmt.Sprintf("Expected to hit all %d app instances in %d attempts, but didn't", instances, maxAttempts))
}

func allTrue(bools []bool) bool {
	for _, curr := range bools {
		if !curr {
			return false
		}
	}
	return true
}

func getBodySkipSSL(skip bool, url string) (string, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: skip},
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Get(url)

	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
