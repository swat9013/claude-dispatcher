package loop

import "testing"

func TestEveryClaimPhaseIsWrittenToTheStatusFile(t *testing.T) {
	for p := phaseRunning; p <= phaseWaitingRetry; p++ {
		if _, ok := phases[p]; !ok {
			t.Errorf("段階 %d を状態 file の綴りに写さない", p)
		}
	}
}
