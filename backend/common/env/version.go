package env

import (
	"strings"
	"sync"
	"time"
)

var (
	defaultGitCommitSHA = "dev"

	buildInfoGitCommitSHA  = ""
	buildInfoGitCommitDate = ""

	bi     buildInfo
	biOnce sync.Once
)

type BuildInfo interface {
	GitCommitSHA() string
	GitCommitDate() *time.Time
}

func GetBuildInfo() buildInfo {
	biOnce.Do(func() {
		if sha := strings.TrimSpace(buildInfoGitCommitSHA); sha == "" {
			bi.gitCommitSHA = defaultGitCommitSHA
		} else {
			bi.gitCommitSHA = sha
		}

		if date, err := time.Parse(time.RFC3339, buildInfoGitCommitDate); err == nil {
			bi.gitCommitDate = &date
		}
	})

	return bi
}

type buildInfo struct {
	gitCommitSHA  string
	gitCommitDate *time.Time
}

var _ BuildInfo = (*buildInfo)(nil)

func (b buildInfo) GitCommitSHA() string {
	return b.gitCommitSHA
}

func (b buildInfo) GitCommitDate() *time.Time {
	return b.gitCommitDate
}
