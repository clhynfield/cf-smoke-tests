package autoscaler

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"encoding/json"

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
	var serviceName string
	var expectedNullResponse string

	BeforeEach(func() {
		appName = testConfig.RuntimeApp
		serviceName = testConfig.AutoscalerInstance
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
		It("can be pushed, bound to Autoscaler, autoscaled, and deleted", func() {
			Expect(cf.Cf("push", appName, "-p", SIMPLE_RUBY_APP_BITS_PATH, "-d", testConfig.AppsDomain, "--no-start").Wait(CF_PUSH_TIMEOUT_IN_SECONDS)).To(Exit(0))
			smoke.SetBackend(appName)
			Expect(cf.Cf("start", appName).Wait(CF_PUSH_TIMEOUT_IN_SECONDS)).To(Exit(0))

			runAutoscaleTests(appName, appUrl, serviceName, expectedNullResponse, testConfig)
		})
	})

	Context("windows apps", func() {
		It("can be pushed, bound to Autoscaler, autoscaled, and deleted", func() {
			smoke.SkipIfWindows(testConfig)

			Expect(cf.Cf("push", appName, "-p", SIMPLE_DOTNET_APP_BITS_PATH, "-d", testConfig.AppsDomain, "-s", "windows2012R2", "-b", "hwc_buildpack", "--no-start").Wait(CF_PUSH_TIMEOUT_IN_SECONDS)).To(Exit(0))
			smoke.EnableDiego(appName)
			Expect(cf.Cf("start", appName).Wait(CF_PUSH_TIMEOUT_IN_SECONDS)).To(Exit(0))

			runAutoscaleTests(appName, appUrl, serviceName, expectedNullResponse, testConfig)
		})
	})
})

func runAutoscaleTests(appName, appUrl, serviceName, expectedNullResponse string, testConfig *smoke.Config) {
	httpTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: testConfig.SkipSSLValidation},
	}
	httpClient := &http.Client{Transport: httpTransport}

	systemDomain := strings.TrimLeft(testConfig.ApiEndpoint, "api.")
	autoscalerApi := fmt.Sprintf("https://autoscale.%s/api", systemDomain)

	Eventually(func() (string, error) {
		return getBodySkipSSL(testConfig.SkipSSLValidation, appUrl)
	}, CF_TIMEOUT_IN_SECONDS).Should(ContainSubstring("It just needed to be restarted!"))

	ExpectAppToBindToAutoscaler(appName, serviceName)
	ExpectAppToAutoscaleOnLimitChange(appName, httpClient, autoscalerApi)

	if testConfig.Cleanup {
		Expect(cf.Cf("delete", appName, "-f", "-r").Wait(CF_TIMEOUT_IN_SECONDS)).To(Exit(0))

		Eventually(func() (string, error) {
			return getBodySkipSSL(testConfig.SkipSSLValidation, appUrl)
		}, CF_TIMEOUT_IN_SECONDS).Should(ContainSubstring(string(expectedNullResponse)))
	}
}

func ExpectAppToBindToAutoscaler(appName string, serviceName string) {
	Expect(cf.Cf("bind-service", appName, serviceName).Wait(CF_TIMEOUT_IN_SECONDS)).To(Exit(0))
}

type App struct {
	Entity struct {
		Name                string `json:"name"`
		Guid                string `json:"guid"`
		ServiceBindingsLink string `json:"service_bindings_url"`
	}
}

type Apps struct {
	Resources []App `struct:"resources"`
}

type Binding struct {
	Metadata struct {
		Guid                string `json:"guid"`
	}
}

type Bindings struct {
	Resources []Binding `struct:"resources"`
}

func ExpectAppToAutoscaleOnLimitChange(appName string, httpClient *http.Client, autoscalerApi string) {
	setAutoscalerLimitsOnApp(appName, 5, 5, httpClient, autoscalerApi)
	ExpectAllAppInstancesToStart(appName, 5, 30)
	setAutoscalerLimitsOnApp(appName, 1, 1, httpClient, autoscalerApi)
	ExpectAllAppInstancesToStart(appName, 1, 30)
}

func ExpectAppToAutoscaleDownOnIdle(appName string, httpClient *http.Client, autoscalerApi string) {
	setAutoscalerLimitsOnApp(appName, 2, 2, httpClient, autoscalerApi)
	ExpectAllAppInstancesToStart(appName, 2, 10)
	setAutoscalerLimitsOnApp(appName, 1, 2, httpClient, autoscalerApi)
	ExpectAllAppInstancesToStart(appName, 1, 30)
}

func setAutoscalerLimitsOnApp(appName string, min int, max int, httpClient *http.Client, autoscalerApi string) {
	var bindingGuid string
	bindingGuid = getAutoscalerBindingGuidFromApp(appName)
	Expect(bindingGuid).To(MatchRegexp("^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"))
	bindingUrl := fmt.Sprintf("%s/bindings/%s", autoscalerApi, bindingGuid)
	data := fmt.Sprintf("{\"min_instances\": %d, \"max_instances\": %d, \"enabled\": true}", min, max)
	oauthToken := getCfOauthToken()
	headers := map[string]string{
		"Authorization": oauthToken,
	}
	binding, err := putWithHeaders(httpClient, bindingUrl, data, headers)
	Expect(err).To(BeNil(), "Failed to update Autoscaler limits")
	Expect(binding).To(MatchRegexp(fmt.Sprintf("\"min_instances\": *%d", min)), "Failed to update Autoscaler limits")
	Expect(binding).To(MatchRegexp(fmt.Sprintf("\"max_instances\": *%d", max)), "Failed to update Autoscaler limits")
}

func getCfOauthToken() (string) {
	curlCmd := cf.Cf("oauth-token")
	Eventually(curlCmd, CF_TIMEOUT_IN_SECONDS).Should(Exit(0))
	oauthToken := strings.TrimSpace(string(curlCmd.Out.Contents()))
	return oauthToken
}

func getAutoscalerBindingGuidFromApp(appName string) (string) {
	// testConfig := smoke.GetConfig()
	var bindings Bindings
	var bindingGuid string
	bindingsLink := getBindingsLinkFromApp(appName)
	curlCmd := cf.Cf("curl", bindingsLink)
	Eventually(curlCmd, CF_TIMEOUT_IN_SECONDS).Should(Exit(0))
	Expect(string(curlCmd.Out.Contents())).To(ContainSubstring("total_results"), "no results")
	err := json.Unmarshal([]byte(curlCmd.Out.Contents()), &bindings)
	if err == nil {
		for _, binding := range bindings.Resources {
			bindingGuid = binding.Metadata.Guid
		}
	}
	return bindingGuid
}

func getBindingsLinkFromApp(appName string) (string) {
	var apps Apps
	bindings := ""
	// Expect(cf.Cf("scale", appName, "-i", "2").Wait(CF_SCALE_TIMEOUT_IN_SECONDS)).To(Exit(0))
	// curlCmd := helpers.CurlSkipSSL(true, fmt.Sprintf("https://%s.%s/put/?%s", appName, testConfig.AppsDomain, p.Encode()))
	curlCmd := cf.Cf("curl", "/v2/apps?q=name%3A" + appName)
	Eventually(curlCmd, CF_TIMEOUT_IN_SECONDS).Should(Exit(0))
	Expect(string(curlCmd.Out.Contents())).To(ContainSubstring("total_results"), "no results")
	err := json.Unmarshal([]byte(curlCmd.Out.Contents()), &apps)
	if err == nil {
		for _, app := range apps.Resources {
			if app.Entity.Name == appName {
				bindings = app.Entity.ServiceBindingsLink
			}
		}
	}
	return bindings
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
		time.Sleep(5000 * time.Millisecond)
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

func putWithHeaders(client *http.Client, url string, data string, headers map[string]string) (string, error) {
	req, err := http.NewRequest("PUT", url, strings.NewReader(data))
	for name, value := range headers {
		req.Header.Add(name, value)
	}
	resp, err := client.Do(req)

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
