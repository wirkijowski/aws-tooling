package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"text/tabwriter"
	"time"

	"github.com/ardanlabs/conf/v3"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/codepipeline"
	"github.com/aws/aws-sdk-go/service/s3"
)

var (
	urlRe = regexp.MustCompile(`https://.*aws\.amazon\.com/s3/home\?region=[a-zA-Z]{2,3}-[a-zA-Z]+-[0-9]+#`)
	revRe = regexp.MustCompile(`Amazon S3 version id: .*`)
)

type Cfg struct {
	Region       string        `conf:"default:us-east-1"`
	PipelineName string        `conf:""`
	Bucket       string        `conf:""`
	Key          string        `conf:"default:version.zip"`
	Timeout      time.Duration `conf:"default:1m"`
}

func main() {
	type stageDetails struct {
		name            string
		executionId     string
		status          string
		revisionId      string
		versionDeployed string
		commit          string
	}

	// =========================================================================
	// Configuration
	var cfg Cfg

	const prefix = "verdeployed"
	help, err := conf.Parse(prefix, &cfg)
	if err != nil {
		if errors.Is(err, conf.ErrHelpWanted) {
			fmt.Println(help)
			os.Exit(0)
		}
		fmt.Printf("parsing config: %v", err)
		os.Exit(1)
	}

	// initialize tabwriter
	w := new(tabwriter.Writer)
	// minwidth, tabwidth, padding, padchar, flags
	w.Init(os.Stdout, 8, 8, 0, '\t', 0)

	defer w.Flush()

	// =========================================================================
	// AWS Session
	sess, err := session.NewSession(aws.NewConfig().WithRegion(cfg.Region))
	if err != nil {
		fmt.Printf("session error: %v", err)
		os.Exit(1)
	}

	// =========================================================================
	// Codepipeline state
	pipelnsvc := codepipeline.New(sess)
	pipelnStateInput := &codepipeline.GetPipelineStateInput{
		Name: aws.String(cfg.PipelineName),
	}

	state, err := pipelnsvc.GetPipelineState(pipelnStateInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			default:
				fmt.Printf("failed to get pipeline state: %s\n", aerr.Message())
			}
		} else {
			fmt.Println(err.Error())
		}
		os.Exit(1)
	}
	var execId, revid string

	fmt.Fprintf(w, "%s\t%s\t%s\t\t%s\n", "Stage", "Status", "Version", "ExecutionID")
	fmt.Fprintf(w, "%s\t%s\t%s\t\t%s\n", "----", "----", "----", "----")

	// Get&Print every stage details
	for _, stage := range state.StageStates {
		// Get revision id from current pipeline execution
		// This can be get for Source stage only (?)
		if *stage.StageName == "Source" {
			for _, astate := range stage.ActionStates {
				if urlRe.MatchString(*astate.EntityUrl) {
					revid = *astate.CurrentRevision.RevisionId
					break
				}
			}
			// Also
			execId = *stage.LatestExecution.PipelineExecutionId
		}
		// save stage details
		details := stageDetails{
			name:        *stage.StageName,
			executionId: *stage.LatestExecution.PipelineExecutionId,
			status:      *stage.LatestExecution.Status,
		}
		// if stage is from current pipeline execution save revision Id
		if execId == details.executionId {
			details.revisionId = revid
			// if stage was executed earlier - not in this run - retrieve
			// revision id from that execution
		} else {
			pipelineExecutionInput := &codepipeline.GetPipelineExecutionInput{
				PipelineExecutionId: &details.executionId,
				PipelineName:        &cfg.PipelineName,
			}

			execution, err := pipelnsvc.GetPipelineExecution(pipelineExecutionInput)
			if err != nil {
				if aerr, ok := err.(awserr.Error); ok {
					switch aerr.Code() {
					default:
						fmt.Println(aerr.Error())
					}
				} else {
					// Prin the error, cat err to awserr.Error to get the Code and
					// Message from an error.
					fmt.Println(err.Error())
				}
				os.Exit(1)
			}
			// finally, save revisionId from earlier execution
			for _, revision := range execution.PipelineExecution.ArtifactRevisions {
				if revRe.MatchString(*revision.RevisionSummary) {
					details.revisionId = *revision.RevisionId
				}
			}

		}
		meta, err := getMetadataFromRevision(sess, cfg, details.revisionId)
		if err != nil {
			fmt.Printf("get metadata from file revision: %v\n", err)
			os.Exit(1)
		}

		details.versionDeployed = *meta["Version"]
		details.commit = *meta["Commit"]

		fmt.Fprintf(w, "%s\t%s\t%s\t\t%s\n", details.name, details.status, details.versionDeployed, details.executionId)
	}
}

func getMetadataFromRevision(s *session.Session, cfg Cfg, ver string) (map[string]*string, error) {
	// =========================================================================
	// S3 client
	svc := s3.New(s)

	input := &s3.HeadObjectInput{
		Bucket:    aws.String(cfg.Bucket),
		Key:       aws.String(cfg.Key),
		VersionId: aws.String(ver),
	}

	result, err := svc.HeadObject(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			default:
				return make(map[string]*string), fmt.Errorf("failed to retrieve version metadata: %s", aerr.Message())
			}
		} else {
			return make(map[string]*string), err
		}

	}
	return result.Metadata, nil
}
