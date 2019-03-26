package main

import (
	"context"
	"io/ioutil"
	"os"
	"sort"
	"sync"

	"github.com/embarkstudios/kubekite/pkg/buildkite"
	kube "github.com/embarkstudios/kubekite/pkg/kubernetes"

	"github.com/namsral/flag"
	"github.com/op/go-logging"
	"gopkg.in/yaml.v2"
)

type JobTemplateTags struct {
	template string
	filters  []string
}

type JobTemplateMapping struct {
	jobManager kube.KubeJobManager
	filters    []string
}

var log = logging.MustGetLogger("kubekite")

func main() {

	var debug bool

	var bkAPIToken string
	var bkOrg string
	var bkQueue string

	var kubeconfig string
	var kubeNamespace string
	var jobTemplateYaml string
	var jobMappingYaml string
	var kubeTimeout int

	var format = logging.MustStringFormatter(
		`%{color}%{time:15:04:05.000} %{shortfile} ▶ %{level:.4s} %{id:03x}%{color:reset} %{message}`,
	)

	logBackend := logging.NewLogBackend(os.Stderr, "", 0)
	logBackendFormatter := logging.NewBackendFormatter(logBackend, format)
	logging.SetBackend(logBackendFormatter)

	flag.BoolVar(&debug, "debug", false, "Turn on debugging")

	flag.StringVar(&bkAPIToken, "buildkite-api-token", "", "Buildkite API token")
	flag.StringVar(&bkOrg, "buildkite-org", "", "Your buildkite organization")
	flag.StringVar(&bkQueue, "buildkite-queue", "", "Buildkite queue to watch for new jobs")

	flag.StringVar(&kubeconfig, "kube-config", "", "Path to your kubeconfig file")
	flag.StringVar(&kubeNamespace, "kube-namespace", "default", "Kubernetes namespace to run jobs in")
	flag.StringVar(&jobTemplateYaml, "job-template", "job-linux.yaml", "Path to your job template YAML file")
	flag.StringVar(&jobMappingYaml, "job-mapping", "", "Path to your job mapping YAML file")
	flag.IntVar(&kubeTimeout, "kube-timeout", 15, "Timeout (in seconds) for Kubernetes API requests. Set to 0 for no timeout.  Default: 15")

	flag.Parse()

	var mappings []JobTemplateTags

	if bkAPIToken == "" {
		log.Fatal("Error: must provide API token via -api-token flag or BUILDKITE_API_TOKEN environment variable")
	}

	if bkOrg == "" {
		log.Fatal("Error: must provide a Buildkite organization via -buildkite-org flag or BUILDKITE_ORG environment variable")
	}

	if bkQueue == "" {
		log.Fatal("Error: must provide a Buildkite queue via -buildkite-queue flag or BUILDKITE_QUEUE environment variable")
	}

	if jobTemplateYaml == "" {
		if jobMappingYaml != "" {
			yml, err := ioutil.ReadFile(jobMappingYaml)

			if err != nil {
				log.Fatal("Error: Failed to read job mapping yaml", err)
			}

			err = yaml.Unmarshal(yml, &mappings)

			if err != nil {
				log.Fatal("Error: Failed to parse job mapping yaml", err)
			}
		} else {
			log.Fatal("Error: must provide a Kuberenetes job template filename via -job-template flag or JOB_TEMPLATE environment variable")
		}
	} else {
		mappings = append(mappings, JobTemplateTags{
			template: jobTemplateYaml,
			filters:  make([]string, 0),
		})
	}

	sigs := make(chan os.Signal, 1)
	done := make(chan struct{}, 1)

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	wg := new(sync.WaitGroup)

	jobManagers := make([]JobTemplateMapping, len(mappings))

	for i := 0; i < len(mappings); i++ {
		jobManager, err := kube.NewKubeJobManager(ctx, wg, mappings[i].template, kubeconfig, kubeNamespace, kubeTimeout, bkOrg)
		if err != nil {
			log.Fatal("Error starting job manager:", err)
		}

		filters := mappings[i].filters
		sort.Strings(filters)

		jobManagers[i] = JobTemplateMapping{
			filters:    filters,
			jobManager: *jobManager,
		}
	}

	bkc, err := buildkite.NewBuildkiteClient(bkAPIToken, debug)
	if err != nil {
		log.Fatal("Error starting Buildkite API client:", err)
	}

	jobChan := buildkite.StartBuildkiteWatcher(ctx, wg, bkc, bkOrg, bkQueue)

	go func(cancel context.CancelFunc) {
		// If we get a SIGINT or SIGTERM, cancel the context and unblock 'done'
		// to trigger a program shutdown
		<-sigs
		cancel()
		close(done)
	}(cancel)

	for {
		select {
		case job := <-jobChan:
			// Preserves the previous behavior of just specifying one possible job template
			if len(jobManagers) == 1 {
				err := jobManagers[0].jobManager.LaunchJob(job.ID)
				if err != nil {
					log.Error("Error launching job:", err)
				}
			} else {
				highestScore := -1
				index := -1

				// Just do exact matching for now
				for i := 0; i < len(jobManagers); i++ {
					score := 0
					for _, filter := range job.Tags {
						ind := sort.SearchStrings(jobManagers[i].filters, filter)

						if ind < len(jobManagers[i].filters) {
							score++
						}
					}

					if score > highestScore {
						highestScore = score
						index = i
					}
				}

				if index >= 0 && index < len(jobManagers) {
					err := jobManagers[index].jobManager.LaunchJob(job.ID)
					if err != nil {
						log.Error("Error launching job:", err)
					}
				}
			}
		case <-ctx.Done():
			log.Notice("Cancellation request recieved. Cancelling job processor.")
			return
		}
	}

}
