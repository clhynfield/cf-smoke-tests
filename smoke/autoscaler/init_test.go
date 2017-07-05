package autoscaler

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	ginkgoconfig "github.com/onsi/ginkgo/config"
	"github.com/onsi/ginkgo/reporters"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gexec"

	"github.com/cloudfoundry-incubator/cf-test-helpers/cf"
	"github.com/cloudfoundry-incubator/cf-test-helpers/workflowhelpers"
	"github.com/cloudfoundry/cf-smoke-tests/smoke"
)

const (
	SIMPLE_RUBY_APP_BITS_PATH = "../../assets/ruby_simple"

	SIMPLE_DOTNET_APP_BITS_PATH = "../../assets/dotnet_simple/Published"

	CF_API_TIMEOUT = 1 * time.Minute

	// timeout for most cf cli calls
	CF_TIMEOUT_IN_SECONDS = 30

	// timeout for cf push cli calls
	CF_PUSH_TIMEOUT_IN_SECONDS = 300

	// timeout for cf scale cli calls
	CF_SCALE_TIMEOUT_IN_SECONDS = 120

	// timeout for cf app cli calls
	CF_APP_STATUS_TIMEOUT_IN_SECONDS = 120
)

func TestSmokeTests(t *testing.T) {
	const autoscalerInstance = "autoscaler-cf-smoke-tests"
	RegisterFailHandler(Fail)

	testConfig := smoke.GetConfig()
	testSetup := workflowhelpers.NewTestSuiteSetup(testConfig)

	SynchronizedBeforeSuite(func() []byte {
		testSetup.Setup()

		Expect(cf.Cf("create-service", "app-autoscaler", "standard", autoscalerInstance).Wait(CF_TIMEOUT_IN_SECONDS)).To(Exit(0))

		Expect(cf.Cf("service", autoscalerInstance).Wait(CF_TIMEOUT_IN_SECONDS)).To(Exit(0))

		return nil
	}, func(data []byte) {})

	SynchronizedAfterSuite(func() {
	}, func() {
		Expect(cf.Cf("delete-service", "-f", autoscalerInstance).Wait(CF_TIMEOUT_IN_SECONDS)).To(Exit(0))

		Expect(cf.Cf("service", autoscalerInstance).Wait(CF_TIMEOUT_IN_SECONDS)).To(Exit(1))

		testSetup.Teardown()
	})

	rs := []Reporter{}

	if testConfig.ArtifactsDirectory != "" {
		os.Setenv("CF_TRACE", traceLogFilePath(testConfig))
		rs = append(rs, reporters.NewJUnitReporter(jUnitReportFilePath(testConfig)))
	}

	if testConfig.Reporter == "TeamCity" {
		rs = append(rs, reporters.NewTeamCityReporter(GinkgoWriter))
	}

	RunSpecsWithDefaultAndCustomReporters(t, "CF-Autoscaler-Smoke-Tests", rs)
}

func traceLogFilePath(testConfig *smoke.Config) string {
	return filepath.Join(testConfig.ArtifactsDirectory, fmt.Sprintf("CF-TRACE-%s-%d.txt", testConfig.SuiteName, ginkgoNode()))
}

func jUnitReportFilePath(testConfig *smoke.Config) string {
	return filepath.Join(testConfig.ArtifactsDirectory, fmt.Sprintf("junit-%s-%d.xml", testConfig.SuiteName, ginkgoNode()))
}

func ginkgoNode() int {
	return ginkgoconfig.GinkgoConfig.ParallelNode
}

func quotaName(prefix string) string {
	return prefix + "_QUOTA"
}
