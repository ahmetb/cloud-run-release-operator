package rollout

import (
	"io/ioutil"

	runapi "github.com/GoogleCloudPlatform/cloud-run-release-operator/internal/run"
	"github.com/GoogleCloudPlatform/cloud-run-release-operator/pkg/config"
	"github.com/GoogleCloudPlatform/cloud-run-release-operator/pkg/service"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"google.golang.org/api/run/v1"
)

// Rollout is the rollout manager.
type Rollout struct {
	RunClient   runapi.Client
	Project     string
	ServiceName string
	Region      string
	Log         *logrus.Entry
	Strategy    *config.Strategy

	// Used to determine if candidate should become stable during update.
	promoteToStable bool
}

// Automatic tags.
const (
	StableTag    = "stable"
	CandidateTag = "candidate"
	LatestTag    = "latest"
)

// New returns a new rollout manager.
func New(client *service.Client, strategy *config.Strategy) *Rollout {
	logger := logrus.New()
	logger.SetOutput(ioutil.Discard)

	return &Rollout{
		RunClient:   client.RunClient,
		Project:     client.Project,
		ServiceName: client.ServiceName,
		Region:      client.Region,
		Strategy:    strategy,
		Log:         logger.WithField("project", client.Project),
	}
}

// WithLogger updates the logger in the rollout instance.
func (r *Rollout) WithLogger(logger *logrus.Logger) *Rollout {
	r.Log = logger.WithField("project", r.Project)
	return r
}

// Rollout handles the gradual rollout.
func (r *Rollout) Rollout() (bool, error) {
	project := r.Project
	serviceID := r.ServiceName
	region := r.Region

	r.Log = r.Log.WithFields(logrus.Fields{
		"project": project,
		"service": serviceID,
		"region":  region,
	})

	svc, err := r.UpdateService(project, serviceID)
	if err != nil {
		return false, errors.Wrapf(err, "failed to perform rollout")
	}

	// Service is non-nil only when the replacement of the service succeded.
	return (svc != nil), nil
}

// UpdateService changes the traffic configuration for the revisions and update
// the service.
func (r *Rollout) UpdateService(project, serviceID string) (*run.Service, error) {
	svc, err := r.RunClient.Service(project, serviceID)
	if err != nil {
		return nil, errors.Wrapf(err, "could not get service %q", serviceID)
	}
	if svc == nil {
		return nil, errors.Errorf("service %q does not exist", serviceID)
	}

	stable := DetectStableRevisionName(svc)
	if stable == "" {
		r.Log.Info("Could not determine stable revision")
		return nil, nil
	}

	candidate := DetectCandidateRevisionName(svc, stable)
	if candidate == "" {
		r.Log.Info("Could not determine candidate revision")
		return nil, nil
	}

	svc = r.SplitTraffic(svc, stable, candidate)
	svc = r.updateAnnotations(svc, stable, candidate)
	svc, err = r.RunClient.ReplaceService(project, serviceID, svc)
	if err != nil {
		return nil, errors.Wrapf(err, "could not update service %q", serviceID)
	}

	return svc, nil
}

// SplitTraffic changes the traffic configuration of the service.
//
// It creates a new traffic configuration for the service. It creates a new
// traffic configuration for the candidate and stable revisions.
// The method respects user-defined revision tags.
func (r *Rollout) SplitTraffic(svc *run.Service, stable, candidate string) *run.Service {

	var traffic []*run.TrafficTarget
	var stablePercent int64

	candidateTraffic, promoteCandidateToStable := r.newCandidateTraffic(svc, candidate)
	if promoteCandidateToStable {
		r.promoteToStable = true
		candidateTraffic.Tag = StableTag
	} else {
		// If candidate is not being promoted, also include traffic
		// configuration for stable revision.
		stablePercent = 100 - candidateTraffic.Percent
		stableTraffic := newTrafficTarget(stable, stablePercent, StableTag)
		traffic = append(traffic, stableTraffic)
	}
	traffic = append(traffic, candidateTraffic)

	// Respect tags manually introduced by the user (e.g. UI/gcloud).
	customTags := userDefinedTrafficTags(svc)
	traffic = append(traffic, customTags...)

	// Always assign latest tag to the latest revision.
	traffic = append(traffic, &run.TrafficTarget{LatestRevision: true, Tag: LatestTag})

	if !r.promoteToStable {
		r.Log.Infof("Assigning %d%% of the traffic to stable revision %s", stablePercent, stable)
		r.Log.Infof("Assigning %d%% of the traffic to candidate revision %s", candidateTraffic.Percent, candidate)
	} else {
		r.Log.Infof("Making revision %s stable", candidate)
	}

	svc.Spec.Traffic = traffic

	return svc
}

// newCandidateTraffic returns the next candidate's traffic configuration.
//
// It also checks if the candidate should be promoted to stable in the next
// update and returns a boolean about that.
func (r *Rollout) newCandidateTraffic(svc *run.Service, candidate string) (*run.TrafficTarget, bool) {
	var promoteToStable bool
	var candidatePercent int64
	candidateTarget := r.currentCandidateTraffic(svc, candidate)
	if candidateTarget == nil {
		candidatePercent = r.Strategy.Steps[0]
	} else {
		candidatePercent = r.nextCandidateTraffic(candidateTarget.Percent)

		// If the traffic share did not change, candidate already handled 100%
		// and is now ready to become stable.
		if candidatePercent == candidateTarget.Percent {
			promoteToStable = true
		}
	}

	candidateTarget = newTrafficTarget(candidate, candidatePercent, CandidateTag)

	return candidateTarget, promoteToStable
}

// userDefinedTrafficTags returns the traffic configurations that include tags
// that were defined by the user (e.g. UI/gcloud).
func userDefinedTrafficTags(svc *run.Service) []*run.TrafficTarget {
	var traffic []*run.TrafficTarget
	for _, target := range svc.Spec.Traffic {
		if target.Tag != "" && !target.LatestRevision &&
			target.Tag != StableTag && target.Tag != CandidateTag {

			traffic = append(traffic, target)
		}
	}

	return traffic
}

// currentCandidateTraffic returns the traffic configuration for the candidate.
func (r *Rollout) currentCandidateTraffic(svc *run.Service, candidate string) *run.TrafficTarget {
	for _, target := range svc.Status.Traffic {
		if target.RevisionName == candidate && target.Percent > 0 {
			return target
		}
	}

	return nil
}

// nextCandidateTraffic calculates the next traffic share for the candidate.
func (r *Rollout) nextCandidateTraffic(current int64) int64 {
	for _, step := range r.Strategy.Steps {
		if step > current {
			return step
		}
	}

	return 100
}

// updateAnnotations updates the annotations to keep some state about the rollout.
func (r *Rollout) updateAnnotations(svc *run.Service, stable, candidate string) *run.Service {
	if svc.Metadata.Annotations == nil {
		svc.Metadata.Annotations = make(map[string]string)
	}

	// The candidate has become the stable revision.
	if r.promoteToStable {
		svc.Metadata.Annotations[StableRevisionAnnotation] = candidate
		delete(svc.Metadata.Annotations, CandidateRevisionAnnotation)

		return svc
	}

	svc.Metadata.Annotations[StableRevisionAnnotation] = stable
	svc.Metadata.Annotations[CandidateRevisionAnnotation] = candidate

	return svc
}

// newTrafficTarget returns a new traffic target instance.
func newTrafficTarget(revision string, percent int64, tag string) *run.TrafficTarget {
	return &run.TrafficTarget{
		RevisionName: revision,
		Percent:      percent,
		Tag:          tag,
	}
}