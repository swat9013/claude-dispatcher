package gitlab

import (
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// mergeRequestJSON は REST の merge request の応答のうち読むもの。一覧の応答には head_pipeline が無いので、1 本ずつ
// 読み直した応答を正規化する (承認と discussion は、どのみち別の endpoint で 1 本ずつ読む)。
type mergeRequestJSON struct {
	IID          int       `json:"iid"`
	Title        string    `json:"title"`
	WebURL       string    `json:"web_url"`
	State        string    `json:"state"`
	CreatedAt    time.Time `json:"created_at"`
	Labels       []string  `json:"labels"`
	Draft        bool      `json:"draft"`
	SourceBranch string    `json:"source_branch"`
	// SourceProjectID は source branch のある project。source の fork が消えていれば null
	SourceProjectID     *int   `json:"source_project_id"`
	TargetProjectID     int    `json:"target_project_id"`
	HasConflicts        bool   `json:"has_conflicts"`
	DetailedMergeStatus string `json:"detailed_merge_status"`
	HeadPipeline        *struct {
		Status string `json:"status"`
	} `json:"head_pipeline"`
	Author struct {
		ID int `json:"id"`
	} `json:"author"`
}

// discussionJSON は merge request の discussion 1 本のうち読むもの。
type discussionJSON struct {
	Notes []struct {
		System     bool `json:"system"`
		Resolvable bool `json:"resolvable"`
		Resolved   bool `json:"resolved"`
		Author     struct {
			ID int `json:"id"`
		} `json:"author"`
	} `json:"notes"`
}

// openMergeRequests は project の open な merge request を全件読み、1 本ずつ読み直して正規化する。失敗は *target.Failure。
func (s Store) openMergeRequests() ([]target.CL, error) {
	out, err := s.get(s.endpoint("merge_requests?state=opened&order_by=created_at&sort=asc&per_page=100"), "--paginate")
	if err != nil {
		return nil, s.fail(classify(err), err)
	}
	listed, err := pages[struct {
		IID int `json:"iid"`
	}](out)
	if err != nil {
		return nil, s.fail(target.Unavailable, err)
	}
	known := collaborators{}
	cls := []target.CL{}
	for _, m := range listed {
		cl, state, err := s.mergeRequest(m.IID, known)
		if err != nil {
			return nil, err
		}
		// 一覧を読んでから読み直すまでに opened でなくなったもの (merge・close・merge の処理中の locked) は open な一覧に
		// 載せない。locked の branch へ手直しの worker を送らない
		if state == "opened" {
			cls = append(cls, cl)
		}
	}
	return cls, nil
}

// mergeRequest は merge request 1 本を読み直し、CL と state を返す。merge か close されていれば Closed。消えたものも
// Closed として返す (state は "")。locked (merge の処理中) は終端にしない。merge に失敗すると opened に戻るので、走っている
// worker を止めて workspace を消さない。失敗は *target.Failure。
func (s Store) mergeRequest(iid int, known collaborators) (target.CL, string, error) {
	base := "merge_requests/" + strconv.Itoa(iid)
	out, err := s.get(s.endpoint(base))
	if gone(err) {
		return target.CL{Number: iid, Closed: true}, "", nil
	}
	if err != nil {
		return target.CL{}, "", s.fail(classify(err), err)
	}
	m, err := object[mergeRequestJSON](out)
	if err != nil {
		return target.CL{}, "", s.fail(target.Unavailable, err)
	}
	if m.State == "merged" || m.State == "closed" {
		return target.CL{Number: iid, Closed: true}, m.State, nil
	}
	cl, err := s.normalizeMergeRequest(base, m, known)
	return cl, m.State, err
}

// normalizeMergeRequest は open な merge request 1 本を、承認と discussion を読み足して CL の形に写す。
func (s Store) normalizeMergeRequest(base string, m mergeRequestJSON, known collaborators) (target.CL, error) {
	// detailed_merge_status の無い GitLab は対象の版より古い。conflict を計算中のものを conflict なしと読まないよう失敗にする
	if m.DetailedMergeStatus == "" {
		return target.CL{}, s.fail(target.Unavailable, errors.New("merge request の応答に detailed_merge_status が無い (GitLab が対象の版より古い)"))
	}
	approved, err := s.approved(base)
	if err != nil {
		return target.CL{}, err
	}
	reviewUnresolved, err := s.reviewUnresolved(base, known)
	if err != nil {
		return target.CL{}, err
	}
	authorIsCollaborator, err := s.isCollaborator(m.Author.ID, known)
	if err != nil {
		return target.CL{}, err
	}
	cl := target.CL{
		Number:               m.IID,
		Title:                m.Title,
		URL:                  m.WebURL,
		CreatedAt:            m.CreatedAt,
		Labels:               m.Labels,
		AuthorIsCollaborator: authorIsCollaborator,
		Head:                 m.SourceBranch,
		SameRepo:             m.SourceProjectID != nil && *m.SourceProjectID == m.TargetProjectID,
		Draft:                m.Draft,
		Mergeable:            mergeability(m),
		ReviewUnresolved:     reviewUnresolved,
		// canceled は人が意図して止めた pipeline なので失敗に数えない。pipeline が無ければ失敗ではない
		CIFailed: m.HeadPipeline != nil && m.HeadPipeline.Status == "failed",
		Approved: approved,
	}
	// fork は project の id で見分ける (GitHub の head の repo の名前に当たる)。source の fork が消えていれば空にし、
	// 曖昧さを数えるときに数えない (formats.md §2.4)
	if m.SourceProjectID != nil {
		cl.HeadRepo = "project:" + strconv.Itoa(*m.SourceProjectID)
	}
	return cl, nil
}

// checkingStatuses は、GitLab が mergeability を計算し終えていない detailed_merge_status
var checkingStatuses = []string{"checking", "unchecked", "preparing"}

// mergeability は merge request の conflict の有無 (cl.conflict) を決める。has_conflicts が立っていれば conflict、
// そうでなく計算中なら未決 (次の tick で見直す)、どちらでもなければ conflict なし。
func mergeability(m mergeRequestJSON) target.Mergeability {
	switch {
	case m.HasConflicts:
		return target.MergeConflict
	case slices.Contains(checkingStatuses, m.DetailedMergeStatus):
		return target.MergeUnknown
	}
	return target.MergeClean
}

// approved は merge request が承認済みか。CE では 1 人以上の承認、有償の tier では承認ルールの充足で、host の判断に従う。
func (s Store) approved(base string) (bool, error) {
	out, err := s.get(s.endpoint(base + "/approvals"))
	if err != nil {
		return false, s.fail(classify(err), err)
	}
	approvals, err := object[struct {
		Approved bool `json:"approved"`
	}](out)
	if err != nil {
		return false, s.fail(target.Unavailable, err)
	}
	return approvals.Approved, nil
}

// reviewUnresolved は、collaborator が書いた未解決の discussion が 1 本以上あるか (cl.review_unresolved)。discussion の
// 書き手は最初の note の作者で、最初の note が system note の discussion は数えない。解決できる note を持ち、そのどれかが
// 未解決なら未解決の discussion とする。
func (s Store) reviewUnresolved(base string, known collaborators) (bool, error) {
	out, err := s.get(s.endpoint(base+"/discussions?per_page=100"), "--paginate")
	if err != nil {
		return false, s.fail(classify(err), err)
	}
	discussions, err := pages[discussionJSON](out)
	if err != nil {
		return false, s.fail(target.Unavailable, err)
	}
	for _, d := range discussions {
		if len(d.Notes) == 0 || d.Notes[0].System || !unresolved(d) {
			continue
		}
		collaborator, err := s.isCollaborator(d.Notes[0].Author.ID, known)
		if err != nil {
			return false, err
		}
		if collaborator {
			return true, nil
		}
	}
	return false, nil
}

// unresolved は discussion が解決できて、まだ解決されていないか。
func unresolved(d discussionJSON) bool {
	for _, n := range d.Notes {
		if n.Resolvable && !n.Resolved {
			return true
		}
	}
	return false
}
