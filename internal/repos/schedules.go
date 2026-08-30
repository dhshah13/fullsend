package repos

import "github.com/fullsend-ai/fullsend/internal/forge"

// ScheduleSpec defines a fullsend pipeline schedule for GitLab repos.
// All sites that create, detect, or match pipeline schedules must use
// PipelineScheduleSpecs to stay in sync.
type ScheduleSpec struct {
	// ComponentName is the probe component name (e.g., "schedule:slash-poll").
	ComponentName string
	// Description is the human-readable schedule description stored in GitLab.
	Description string
	// Cron is the cron expression for the schedule.
	Cron string
	// Variables are the CI/CD variables set on the schedule trigger.
	Variables map[string]string
}

// PipelineScheduleSpecs is the canonical list of fullsend pipeline
// schedules for GitLab repos. convergeSchedules, ProbeComponents, and
// setupGitLabPipelineSchedules all reference this slice so that schedule
// definitions stay in one place.
var PipelineScheduleSpecs = []ScheduleSpec{
	{
		ComponentName: "schedule:slash-poll",
		Description:   "fullsend slash poll",
		Cron:          "*/5 * * * *",
		Variables:     map[string]string{forge.VarPollMode: "slash"},
	},
	{
		ComponentName: "schedule:event-poll",
		Description:   "fullsend event poll",
		Cron:          "2,17,32,47 * * * *",
		Variables:     map[string]string{forge.VarPollMode: "events"},
	},
}

// scheduleSpecByComponent returns the ScheduleSpec for the given
// component name, or nil if not found.
func scheduleSpecByComponent(name string) *ScheduleSpec {
	for i := range PipelineScheduleSpecs {
		if PipelineScheduleSpecs[i].ComponentName == name {
			return &PipelineScheduleSpecs[i]
		}
	}
	return nil
}
