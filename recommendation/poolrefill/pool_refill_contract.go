package poolrefill

import (
	"context"
	"strings"

	"sea/config"

	types "sea/type"
)

type PoolRefillRunner interface {
	Run(ctx context.Context, job types.PoolRefillJob) (types.PoolRefillRunResult, error)
}

func normalizePoolRefillJob(job types.PoolRefillJob, queryFanout int) types.PoolRefillJob {
	job.UserID = strings.TrimSpace(job.UserID)
	job.PoolType = strings.TrimSpace(job.PoolType)
	job.PeriodBucket = strings.TrimSpace(job.PeriodBucket)
	job.QueryTexts = mergeQueryTexts(nil, job.QueryTexts, queryFanout)
	return job
}

func poolRefillJobKey(job types.PoolRefillJob) string {
	return strings.Join([]string{
		strings.TrimSpace(job.UserID),
		strings.TrimSpace(job.PoolType),
		strings.TrimSpace(job.PeriodBucket),
	}, "|")
}

func poolPolicyFor(poolType string) config.PoolPolicy {
	switch poolType {
	case "long_term":
		return config.Cfg.Pools.LongTerm
	case "short_term":
		return config.Cfg.Pools.ShortTerm
	case "periodic":
		return config.Cfg.Pools.Periodic
	default:
		return config.Cfg.Pools.ShortTerm
	}
}
