package buildkite

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/buildkite/go-buildkite/buildkite"
	logging "github.com/op/go-logging"
)

var log = logging.MustGetLogger("kubekite")

func init() {

	var format = logging.MustStringFormatter(
		`%{color}%{time:15:04:05.000} %{shortfile} ▶ %{level:.4s} %{id:03x}%{color:reset} %{message}`,
	)

	logBackend := logging.NewLogBackend(os.Stderr, "", 0)
	logBackendFormatter := logging.NewBackendFormatter(logBackend, format)
	logging.SetBackend(logBackend, logBackendFormatter)
}

// NewBuildkiteClient creates and initializes a Buildkite API client to watch for build jobs
func NewBuildkiteClient(bkAPIToken string, debug bool) (*buildkite.Client, error) {

	bkconfig, err := buildkite.NewTokenConfig(bkAPIToken, debug)
	if err != nil {
		return nil, fmt.Errorf("unable to configure a new Buildkite client: %v", err)
	}

	c := buildkite.NewClient(bkconfig.Client())

	return c, nil

}

// StartBuildkiteWatcher starts a watcher that monitors a queue for new jobs
func StartBuildkiteWatcher(ctx context.Context, wg *sync.WaitGroup, client *buildkite.Client, org string, queue string) chan string {
	c := make(chan string, 10)

	go watchBuildkiteJobs(ctx, wg, client, org, queue, c)

	log.Info("Buildkite job watcher started.")

	return c
}

func watchBuildkiteJobs(ctx context.Context, wg *sync.WaitGroup, client *buildkite.Client, org string, queue string, jobChan chan<- string) {
	wg.Add(1)
	defer wg.Done()

	for {

		log.Info("Checking Buildkite API for builds and jobs...")

		builds, _, err := client.Builds.ListByOrg(org, &buildkite.BuildsListOptions{})
		if err != nil {
			log.Error("Error fetching builds from Buildkite API:", err)
		}

		for _, build := range builds {
			for _, job := range build.Jobs {
				if job.State != nil && *job.State == "scheduled" && jobInTargetQueue(*job, queue) {
					jobChan <- *job.ID
				}
			}
		}

		time.Sleep(5 * time.Second)

	}
}

func jobInTargetQueue(job buildkite.Job, queue string) bool {
	targetRule := fmt.Sprintf("queue=%s", queue)

	for _, rule := range job.AgentQueryRules {
		if rule == targetRule {
			return true
		}
	}

	return false
}
