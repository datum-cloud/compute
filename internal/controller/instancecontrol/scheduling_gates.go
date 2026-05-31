package instancecontrol

type SchedulingGate string

const (
	NetworkSchedulingGate SchedulingGate = "Network"
	QuotaSchedulingGate   SchedulingGate = "Quota"

	// ReferencedDataSchedulingGate is stamped on new instances when the workload
	// template references ConfigMaps or Secrets AND the management-plane feature
	// flag EnableReferencedDataGate is enabled. It is cleared by the cell
	// InstanceReconciler once all expected companion objects are present.
	ReferencedDataSchedulingGate SchedulingGate = "ReferencedData"
)

func (s SchedulingGate) String() string {
	return string(s)
}
