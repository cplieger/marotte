package rule

import "slices"

// maxPresses bounds Status.Presses: a retry of an op_id older than its rule's last maxPresses presses evaluates again.
const maxPresses = 10

// RecordPress stores p as its press's first answer, its reason cut as an event string is, dropping the oldest past
// maxPresses, and answers what it stored.
func RecordPress(st *Status, p Press) Press {
	p.Reason, _ = cutString(p.Reason)
	st.Presses = append(st.Presses, p)
	st.Presses = st.Presses[max(0, len(st.Presses)-maxPresses):]
	return p
}

// PressOf answers the stored answer of the press whose event is eventID.
func PressOf(st *Status, eventID string) (Press, bool) {
	i := slices.IndexFunc(st.Presses, func(p Press) bool { return p.EventID == eventID })
	if i < 0 {
		return Press{}, false
	}
	return st.Presses[i], true
}
