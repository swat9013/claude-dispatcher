  - name: resolve-conflict
    on: cl
    when:
      conflict: true
      head: claude-dispatcher/*
      draft: false
    action: |
      merge request !{{ .cl.number }} ({{ .cl.url }}) の conflict を解く。
      source branch {{ .cl.head }} を detach で取り出し (`git fetch origin {{ .cl.head }} && git checkout --detach FETCH_HEAD`)、target branch を取り込んで conflict を解き、テストを通して `git push origin HEAD:{{ .cl.head }}` で push する。
  - name: address-review
    on: cl
    when:
      review_unresolved: true
      head: claude-dispatcher/*
      draft: false
    action: |
      merge request !{{ .cl.number }} ({{ .cl.url }}) の未解決の discussion に応える。
      source branch {{ .cl.head }} を detach で取り出し (`git fetch origin {{ .cl.head }} && git checkout --detach FETCH_HEAD`)、未解決の discussion ごとに、直して `git push origin HEAD:{{ .cl.head }}` で push するか、直さない理由を返信する。応えた discussion は resolve する。
  - name: fix-ci
    on: cl
    when:
      ci_failed: true
      head: claude-dispatcher/*
      draft: false
    action: |
      merge request !{{ .cl.number }} ({{ .cl.url }}) の失敗している pipeline を直す。
      source branch {{ .cl.head }} を detach で取り出し (`git fetch origin {{ .cl.head }} && git checkout --detach FETCH_HEAD`)、失敗した job の log を読んで原因を直し、テストを通して `git push origin HEAD:{{ .cl.head }}` で push する。
  # 承認済みの merge request に当てる trigger (approved: true) は置かない。merge を worker に任せるなら、自分で足す
