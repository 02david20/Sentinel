set -e
S=$(cd "$(dirname "$0")" && pwd); cd $S/demo
export SENTINEL_STATE_DIR=$S/state; rm -rf $SENTINEL_STATE_DIR; B=$S/sentinel
cat > workflows/price-watch.yaml <<'Y'
name: price-watch
every: 4h
write_allow: [Webhook.Post, Snapshot.Commit]
summary: "${changes.added_count} new, ${changes.changed_count} changed."

steps:
  - id: changes
    block: Diff
    in:
      name: price-watch
      items:
        - { id: A-1, sig: v1 }
        - { id: A-2, sig: v1 }
      key: id
      fingerprint: sig

  - id: ids
    block: Brief
    in: { items: "${changes.added}", key: id }

  - id: notify
    block: Webhook.Post
    when: "changes.has_changes"
    idempotency_key: "notify-${changes.digest}"
    in: { text: "New items: ${ids.text}" }

  - id: save
    block: Snapshot.Commit
    when: "changes.has_changes"
    in:
      name: price-watch
      items:
        - { id: A-1, sig: v1 }
        - { id: A-2, sig: v1 }
      key: id
Y
sed -e 's/price-watch/weekly-report/g' workflows/price-watch.yaml > workflows/weekly-report.yaml
$S/venv/bin/python $S/webhook_srv.py & SRV=$!; sleep 1
export SENTINEL_WEBHOOK_URL=http://127.0.0.1:18099/hook
$B run workflows/weekly-report.yaml >/dev/null; $B pause weekly-report >/dev/null
$B run workflows/price-watch.yaml >/dev/null
sed -i '' 's/{ id: A-2, sig: v1 }/{ id: A-2, sig: v2 }\n        - { id: A-3, sig: v1 }/' workflows/price-watch.yaml
env -u SENTINEL_WEBHOOK_URL $B run workflows/price-watch.yaml >/dev/null || true
$B run workflows/price-watch.yaml --dry-run >/dev/null
$B run workflows/price-watch.yaml >/dev/null
kill $SRV
