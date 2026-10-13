package gitlab

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/swat9013/claude-dispatcher/internal/target"
)

// connectionSize は GraphQL の connection を 1 往復で読む件数 (GitLab の上限)。REST の一覧の 1 page と、id で絞る件数にも使う
const connectionSize = 100

// mergeRequestPageSize は open な merge request の一覧を 1 往復で読む本数。1 本ごとに discussion を connectionSize 本まで
// 読むので、page を大きくすると GitLab の問い合わせの時間の上限 (既定 30 秒) に届く。open な merge request が 100 本を超える
// self-managed の GitLab で、100 本と 50 本の page は時間切れで落ち、20 本の page は 7 秒台で通った
const mergeRequestPageSize = 10

// discussionFields は discussion 1 本のうち読むもの。書き手と system note かは最初の note で決まるので、note は先頭だけを読む
const discussionFields = `
        resolvable resolved
        notes(first: 1) { nodes { system maxAccessLevelOfAuthor } }`

// mergeRequestFields は merge request 1 本のうち読むもの。discussion は最初の connectionSize 本を読み、続きは
// discussionsQuery で読み足す。
var mergeRequestFields = fmt.Sprintf(`
      iid title webUrl state createdAt draft sourceBranch sourceProjectId targetProjectId mergeStatusEnum approved
      labels { pageInfo { hasNextPage } nodes { title } }
      headPipeline { status }
      author { id }
      discussions(first: %[1]d) { pageInfo { hasNextPage endCursor } nodes {`+discussionFields+` } }`, connectionSize)

// 問い合わせの operation 名は、どの問い合わせを撃ったかを glab の argv から読めるように付ける
var (
	openMergeRequestsQuery = fmt.Sprintf(`
query OpenMergeRequests($fullPath: ID!, $after: String) {
  project(fullPath: $fullPath) {
    mergeRequests(state: opened, sort: CREATED_ASC, first: %d, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {`+mergeRequestFields+`
      }
    }
  }
}`, mergeRequestPageSize)

	mergeRequestQuery = `
query MergeRequest($fullPath: ID!, $iid: String!) {
  project(fullPath: $fullPath) {
    mergeRequest(iid: $iid) {` + mergeRequestFields + `
    }
  }
}`

	discussionsQuery = fmt.Sprintf(`
query MergeRequestDiscussions($fullPath: ID!, $iid: String!, $after: String) {
  project(fullPath: $fullPath) {
    mergeRequest(iid: $iid) {
      discussions(first: %d, after: $after) { pageInfo { hasNextPage endCursor } nodes {`+discussionFields+` } }
    }
  }
}`, connectionSize)
)

type pageInfo struct {
	HasNextPage bool
	EndCursor   string
}

// mergeRequestNode は mergeRequestFields の応答。
type mergeRequestNode struct {
	IID          string
	Title        string
	WebURL       string `json:"webUrl"`
	State        string
	CreatedAt    time.Time
	Draft        bool
	SourceBranch string
	// SourceProjectID は source branch のある project。source の fork が消えていれば null
	SourceProjectID *int
	TargetProjectID int
	MergeStatusEnum string
	Approved        bool
	Labels          struct {
		PageInfo pageInfo
		Nodes    []struct{ Title string }
	}
	HeadPipeline *struct{ Status string }
	Author       *struct{ ID string }
	Discussions  discussionConnection
}

type discussionConnection struct {
	PageInfo pageInfo
	Nodes    []discussionNode
}

// discussionNode は discussionFields の応答。
type discussionNode struct {
	Resolvable bool
	Resolved   bool
	Notes      struct {
		Nodes []struct {
			System bool
			// MaxAccessLevelOfAuthor は note の作者の project での access level の名前 (group から継承したものを含む)。
			// member でなければ null
			MaxAccessLevelOfAuthor *string
		}
	}
}

// variable は GraphQL の変数 1 つ。
type variable struct{ name, value string }

// graphql は GraphQL の問い合わせを撃ち、応答の data を data に読む。fullPath は project の path で埋める。値が空の変数は
// 渡さない (null になる)。失敗は *target.Failure。
func (s Store) graphql(query string, data any, variables ...variable) error {
	args := []string{"api", "--hostname", s.project.Host, "graphql", "-f", "query=" + query, "-f", "fullPath=" + s.project.Path}
	for _, v := range variables {
		if v.value != "" {
			args = append(args, "-f", v.name+"="+v.value)
		}
	}
	out, err := s.glab.Run(args...)
	if err != nil {
		return s.graphqlFailure(err)
	}
	payload := struct{ Data any }{Data: data}
	if err := json.Unmarshal(out, &payload); err != nil {
		return s.fail(target.Unavailable, fmt.Errorf("glab api graphql の出力を読めない: %w", err))
	}
	return nil
}

// graphqlFailure は GraphQL の失敗を分類する。GitLab が GraphQL の errors で答えると、glab は status を出さずに message だけを
// 出す (認証の失敗も)。message とともに status を出さなかった失敗は、REST で project を 1 回読み、その失敗で分類する。REST が
// 通れば読めない (formats.md §3)。
func (s Store) graphqlFailure(err error) *target.Failure {
	if readFailure(err).apiMessageGiven {
		return s.fail(classify(err), err)
	}
	if _, probe := s.get("projects/" + url.PathEscape(s.project.Path)); probe != nil {
		return s.fail(classify(probe), probe)
	}
	return s.fail(target.Unavailable, err)
}

func (s Store) notVisible() *target.Failure {
	return s.fail(target.NotVisible, fmt.Errorf("glab api graphql の出力に project が無い"))
}

// openMergeRequests は project の open な merge request を全件読み、正規化して返す。失敗は *target.Failure。
func (s Store) openMergeRequests() ([]target.CL, error) {
	var nodes []mergeRequestNode
	for after := ""; ; {
		var data struct {
			Project *struct {
				MergeRequests struct {
					PageInfo pageInfo
					Nodes    []mergeRequestNode
				}
			}
		}
		if err := s.graphql(openMergeRequestsQuery, &data, variable{"after", after}); err != nil {
			return nil, err
		}
		if data.Project == nil {
			return nil, s.notVisible()
		}
		page := data.Project.MergeRequests
		// 一覧の state の絞り込みと別に opened だけを採る。locked (merge の処理中) の branch へ手直しの worker を送らない
		for _, n := range page.Nodes {
			if n.State == "opened" {
				nodes = append(nodes, n)
			}
		}
		if !page.PageInfo.HasNextPage {
			break
		}
		after = page.PageInfo.EndCursor
	}
	return s.normalizeMergeRequests(nodes)
}

// mergeRequest は merge request 1 本を読み直す。merge か close されていれば Closed。消えたものも Closed として返す。locked
// (merge の処理中) は終端にしない。merge に失敗すると opened に戻るので、走っている worker を止めて workspace を消さない。
// 失敗は *target.Failure。
func (s Store) mergeRequest(iid int) (target.CL, error) {
	var data struct {
		Project *struct{ MergeRequest *mergeRequestNode }
	}
	if err := s.graphql(mergeRequestQuery, &data, variable{"iid", strconv.Itoa(iid)}); err != nil {
		return target.CL{}, err
	}
	switch {
	case data.Project == nil:
		return target.CL{}, s.notVisible()
	case data.Project.MergeRequest == nil, data.Project.MergeRequest.State == "merged", data.Project.MergeRequest.State == "closed":
		return target.CL{Number: iid, Closed: true}, nil
	}
	cls, err := s.normalizeMergeRequests([]mergeRequestNode{*data.Project.MergeRequest})
	if err != nil {
		return target.CL{}, err
	}
	return cls[0], nil
}

// normalizeMergeRequests は open な merge request を CL の形に写す。discussion の続きと作者の access level を読み足し、
// conflict を計算し終えていないものには GitLab に計算し直させる (結果は次の観測で読む)。
func (s Store) normalizeMergeRequests(nodes []mergeRequestNode) ([]target.CL, error) {
	authors := make([]int, len(nodes))
	for i := range nodes {
		if err := s.readRemainingDiscussions(&nodes[i]); err != nil {
			return nil, err
		}
		author, err := s.authorID(nodes[i])
		if err != nil {
			return nil, err
		}
		authors[i] = author
	}
	known, err := s.readCollaborators(authors)
	if err != nil {
		return nil, err
	}
	cls := make([]target.CL, len(nodes))
	var unchecked []int
	for i, n := range nodes {
		cl, err := s.normalizeMergeRequest(n, known[authors[i]])
		if err != nil {
			return nil, err
		}
		if cl.Mergeable == target.MergeUnknown {
			unchecked = append(unchecked, cl.Number)
		}
		cls[i] = cl
	}
	if err := s.recheckMergeability(unchecked); err != nil {
		return nil, err
	}
	return cls, nil
}

// readRemainingDiscussions は、最初の page に収まらなかった discussion を読み足す。
func (s Store) readRemainingDiscussions(n *mergeRequestNode) error {
	for n.Discussions.PageInfo.HasNextPage {
		var data struct {
			Project *struct {
				MergeRequest *struct{ Discussions discussionConnection }
			}
		}
		if err := s.graphql(discussionsQuery, &data, variable{"iid", n.IID}, variable{"after", n.Discussions.PageInfo.EndCursor}); err != nil {
			return err
		}
		if data.Project == nil {
			return s.notVisible()
		}
		if data.Project.MergeRequest == nil {
			// 読んでいる間に消えた。次の観測で一覧から外れる
			return s.fail(target.Unavailable, fmt.Errorf("merge request !%s の discussion を読み足す間に merge request が消えた", n.IID))
		}
		next := data.Project.MergeRequest.Discussions
		n.Discussions.Nodes = append(n.Discussions.Nodes, next.Nodes...)
		n.Discussions.PageInfo = next.PageInfo
	}
	return nil
}

// authorID は merge request の作者の user id。作者が読めない (null) なら 0。
func (s Store) authorID(n mergeRequestNode) (int, error) {
	if n.Author == nil {
		return 0, nil
	}
	id, err := strconv.Atoi(n.Author.ID[strings.LastIndex(n.Author.ID, "/")+1:])
	if err != nil {
		return 0, s.fail(target.Unavailable, fmt.Errorf("merge request !%s の作者の id %q を読めない", n.IID, n.Author.ID))
	}
	return id, nil
}

// readCollaborators は user が collaborator か (project で Developer 以上の access level を持つか) を、connectionSize 人ごとに
// 1 往復でまとめて読む。member でない user は false。id 0 (作者が読めない) は読まずに false。
func (s Store) readCollaborators(userIDs []int) (collaborators, error) {
	known := collaborators{0: false}
	var unread []int
	for _, id := range userIDs {
		if _, ok := known[id]; !ok {
			known[id] = false
			unread = append(unread, id)
		}
	}
	for batch := range chunks(unread, connectionSize) {
		query := make([]string, len(batch))
		for i, id := range batch {
			query[i] = "user_ids[]=" + strconv.Itoa(id)
		}
		out, err := s.get(s.endpoint("members/all?" + strings.Join(query, "&") + "&per_page=" + strconv.Itoa(connectionSize)))
		if err != nil {
			return nil, s.fail(classify(err), err)
		}
		listed, err := object[[]struct {
			ID          int `json:"id"`
			AccessLevel int `json:"access_level"`
		}](out)
		if err != nil {
			return nil, s.fail(target.Unavailable, err)
		}
		for _, m := range listed {
			known[m.ID] = m.AccessLevel >= developerAccess
		}
	}
	return known, nil
}

// recheckMergeability は merge request に conflict の計算し直しを GitLab に求める (REST の一覧の with_merge_status_recheck)。
// GraphQL の読み出しは計算し直しを起こさないので、これが無いと計算し終えないまま残る。GitLab が計算し直すのは、glab の user が
// Developer 以上のときだけ。一覧の page と同じ本数ずつ求める (計算し直しを求める本数が多いと応答が遅れ、手前の proxy が 502 で
// 打ち切りうる)。
func (s Store) recheckMergeability(iids []int) error {
	for batch := range chunks(iids, mergeRequestPageSize) {
		query := make([]string, len(batch))
		for i, iid := range batch {
			query[i] = "iids[]=" + strconv.Itoa(iid)
		}
		if _, err := s.get(s.endpoint("merge_requests?" + strings.Join(query, "&") + "&with_merge_status_recheck=true&per_page=" + strconv.Itoa(connectionSize))); err != nil {
			return s.fail(classify(err), err)
		}
	}
	return nil
}

// chunks は list を size 個ずつに分ける。
func chunks(list []int, size int) func(yield func([]int) bool) {
	return func(yield func([]int) bool) {
		for start := 0; start < len(list); start += size {
			if !yield(list[start:min(start+size, len(list))]) {
				return
			}
		}
	}
}

// normalizeMergeRequest は open な merge request 1 本を CL の形に写す。authorIsCollaborator は作者が collaborator か。
func (s Store) normalizeMergeRequest(n mergeRequestNode, authorIsCollaborator bool) (target.CL, error) {
	iid, err := strconv.Atoi(n.IID)
	if err != nil {
		return target.CL{}, s.fail(target.Unavailable, fmt.Errorf("merge request の iid %q を読めない", n.IID))
	}
	if n.Labels.PageInfo.HasNextPage {
		return target.CL{}, s.fail(target.Truncated, fmt.Errorf("merge request !%d の label が %d 個を超える", iid, connectionSize))
	}
	mergeable, ok := mergeability[n.MergeStatusEnum]
	if !ok {
		return target.CL{}, s.fail(target.Unavailable, fmt.Errorf("merge request !%d の mergeStatusEnum %q を読めない", iid, n.MergeStatusEnum))
	}
	labels := make([]string, len(n.Labels.Nodes))
	for i, l := range n.Labels.Nodes {
		labels[i] = l.Title
	}
	cl := target.CL{
		Number:               iid,
		Title:                n.Title,
		URL:                  n.WebURL,
		CreatedAt:            n.CreatedAt,
		Labels:               labels,
		AuthorIsCollaborator: authorIsCollaborator,
		Head:                 n.SourceBranch,
		SameRepo:             n.SourceProjectID != nil && *n.SourceProjectID == n.TargetProjectID,
		Draft:                n.Draft,
		Mergeable:            mergeable,
		ReviewUnresolved:     reviewUnresolved(n.Discussions.Nodes),
		// CANCELED は人が意図して止めた pipeline なので失敗に数えない。pipeline が無ければ失敗ではない
		CIFailed: n.HeadPipeline != nil && n.HeadPipeline.Status == "FAILED",
		Approved: n.Approved,
	}
	// fork は project の id で見分ける (GitHub の head の repo の名前に当たる)。source の fork が消えていれば空にし、
	// 曖昧さを数えるときに数えない (formats.md §2.4)
	if n.SourceProjectID != nil {
		cl.HeadRepo = "project:" + strconv.Itoa(*n.SourceProjectID)
	}
	return cl, nil
}

// mergeability は mergeStatusEnum を merge request の conflict の有無 (cl.conflict) に写す。計算し終えていない値は未決 (次の
// 観測で見直す)。detailedMergeStatus と conflicts は使わない: draft では detailedMergeStatus が DRAFT_STATUS になって計算中かが
// 隠れ、conflicts は計算し直し待ち (CANNOT_BE_MERGED_RECHECK) で false を返す。
var mergeability = map[string]target.Mergeability{
	"CAN_BE_MERGED":            target.MergeClean,
	"CANNOT_BE_MERGED":         target.MergeConflict,
	"UNCHECKED":                target.MergeUnknown,
	"CHECKING":                 target.MergeUnknown,
	"CANNOT_BE_MERGED_RECHECK": target.MergeUnknown,
}

// collaboratorAccess は collaborator と数える access level の名前 (maxAccessLevelOfAuthor)。Developer 以上 (developerAccess)
var collaboratorAccess = map[string]bool{"Developer": true, "Maintainer": true, "Owner": true}

// reviewUnresolved は、collaborator が書いた未解決の discussion が 1 本以上あるか (cl.review_unresolved)。discussion の書き手は
// 最初の note の作者で、最初の note が system note の discussion は数えない。
func reviewUnresolved(discussions []discussionNode) bool {
	for _, d := range discussions {
		if !d.Resolvable || d.Resolved || len(d.Notes.Nodes) == 0 {
			continue
		}
		first := d.Notes.Nodes[0]
		if !first.System && first.MaxAccessLevelOfAuthor != nil && collaboratorAccess[*first.MaxAccessLevelOfAuthor] {
			return true
		}
	}
	return false
}
