package instancecontrol

type SchedulingGate string

const (
	NetworkSchedulingGate SchedulingGate = "Network"
	QuotaSchedulingGate   SchedulingGate = "Quota"
)

func (s SchedulingGate) String() string {
	return string(s)
}
