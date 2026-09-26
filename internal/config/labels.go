package config

// Label は tracker に置く label。Color と Description は setup が作るときに使う。
type Label struct {
	Name        string
	Color       string
	Description string
}

// MechanismLabels は機構が付ける label (dispatcher:wip / ready-for-human / 書いてあれば triage label)。
// tick が config の実在検査で確かめるのはこれだけ (formats.md §2)。着手可 label は人が付けるもので、無くても観測は壊れない。
func (c Config) MechanismLabels() []Label {
	labels := []Label{
		{WIPLabel, "fbca04", "dispatcher: worker が着手中"},
		{HumanLabel, "d93f0b", "dispatcher: 人の判断待ち"},
	}
	if c.TriageLabel != "" {
		labels = append(labels, Label{c.TriageLabel, "ededed", "dispatcher: worker の起票。triage 待ち"})
	}
	return labels
}

// Labels は導入で揃える label (着手可 label と機構の label)。setup が作り、doctor が確かめる。
func (c Config) Labels() []Label {
	return append([]Label{{c.ReadyLabel, "0e8a16", "dispatcher: 着手可"}}, c.MechanismLabels()...)
}
