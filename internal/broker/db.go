package broker

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/scott-pallas/agentswarm/internal/types"
	_ "modernc.org/sqlite"
)

// DB wraps the SQLite database with prepared statements.
type DB struct {
	conn *sql.DB
}

// OpenDB opens (or creates) the SQLite database and initializes the schema.
func OpenDB(path string) (*DB, error) {
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".agentswarm.db")
	}
	conn, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	conn.SetMaxOpenConns(1)
	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func (db *DB) Close() error { return db.conn.Close() }

func (db *DB) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS peers (
  id TEXT PRIMARY KEY,
  pid INTEGER NOT NULL,
  cwd TEXT NOT NULL,
  git_root TEXT,
  git_branch TEXT,
  tty TEXT,
  summary TEXT NOT NULL DEFAULT '',
  active_files TEXT NOT NULL DEFAULT '[]',
  registered_at TEXT NOT NULL,
  last_seen TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL DEFAULT 'message',
  from_id TEXT NOT NULL,
  to_id TEXT,
  thread_id TEXT,
  reply_to INTEGER,
  text TEXT NOT NULL,
  urgency TEXT NOT NULL DEFAULT 'normal',
  context TEXT,
  scope TEXT,
  sent_at TEXT NOT NULL,
  delivered INTEGER NOT NULL DEFAULT 0,
  FOREIGN KEY (from_id) REFERENCES peers(id),
  FOREIGN KEY (reply_to) REFERENCES messages(id)
);

CREATE TABLE IF NOT EXISTS context (
  key TEXT NOT NULL,
  scope_type TEXT NOT NULL DEFAULT 'repo',
  scope_value TEXT NOT NULL,
  value TEXT NOT NULL,
  set_by TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (key, scope_type, scope_value),
  FOREIGN KEY (set_by) REFERENCES peers(id)
);

CREATE INDEX IF NOT EXISTS idx_messages_to_id ON messages(to_id, delivered);
CREATE INDEX IF NOT EXISTS idx_messages_thread ON messages(thread_id);
CREATE INDEX IF NOT EXISTS idx_messages_scope ON messages(scope, delivered);
CREATE INDEX IF NOT EXISTS idx_peers_git_root ON peers(git_root);
CREATE INDEX IF NOT EXISTS idx_peers_cwd ON peers(cwd);
`
	_, err := db.conn.Exec(schema)
	return err
}

// --- Peer operations ---

func (db *DB) InsertPeer(p *types.Peer) error {
	af, _ := json.Marshal(p.ActiveFiles)
	_, err := db.conn.Exec(
		`INSERT INTO peers (id, pid, cwd, git_root, git_branch, tty, summary, active_files, registered_at, last_seen)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.PID, p.CWD, nullStr(p.GitRoot), nullStr(p.GitBranch), nullStr(p.TTY),
		p.Summary, string(af), p.RegisteredAt, p.LastSeen,
	)
	return err
}

func (db *DB) DeletePeer(id string) error {
	_, err := db.conn.Exec(`DELETE FROM peers WHERE id = ?`, id)
	return err
}

func (db *DB) UpdateHeartbeat(id string, activeFiles []string, gitBranch string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if len(activeFiles) > 0 {
		af, _ := json.Marshal(activeFiles)
		_, err := db.conn.Exec(
			`UPDATE peers SET last_seen = ?, active_files = ?, git_branch = COALESCE(NULLIF(?, ''), git_branch) WHERE id = ?`,
			now, string(af), gitBranch, id,
		)
		return err
	}
	_, err := db.conn.Exec(
		`UPDATE peers SET last_seen = ?, git_branch = COALESCE(NULLIF(?, ''), git_branch) WHERE id = ?`,
		now, gitBranch, id,
	)
	return err
}

func (db *DB) SetSummary(id, summary string) error {
	_, err := db.conn.Exec(`UPDATE peers SET summary = ? WHERE id = ?`, summary, id)
	return err
}

func (db *DB) GetPeer(id string) (*types.Peer, error) {
	row := db.conn.QueryRow(`SELECT id, pid, cwd, git_root, git_branch, tty, summary, active_files, registered_at, last_seen FROM peers WHERE id = ?`, id)
	return scanPeer(row)
}

func (db *DB) ListPeers(scope, cwd, gitRoot, excludeID string) ([]types.Peer, error) {
	var rows *sql.Rows
	var err error
	switch scope {
	case "repo":
		rows, err = db.conn.Query(
			`SELECT id, pid, cwd, git_root, git_branch, tty, summary, active_files, registered_at, last_seen
			 FROM peers WHERE git_root = ? AND id != ?`, gitRoot, excludeID)
	case "directory":
		rows, err = db.conn.Query(
			`SELECT id, pid, cwd, git_root, git_branch, tty, summary, active_files, registered_at, last_seen
			 FROM peers WHERE cwd = ? AND id != ?`, cwd, excludeID)
	default: // "machine"
		rows, err = db.conn.Query(
			`SELECT id, pid, cwd, git_root, git_branch, tty, summary, active_files, registered_at, last_seen
			 FROM peers WHERE id != ?`, excludeID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var peers []types.Peer
	for rows.Next() {
		p, err := scanPeerRows(rows)
		if err != nil {
			return nil, err
		}
		peers = append(peers, *p)
	}
	return peers, rows.Err()
}

func (db *DB) AllPeers() ([]types.Peer, error) {
	return db.ListPeers("machine", "", "", "")
}

func (db *DB) PeerCount() (int, error) {
	var n int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM peers`).Scan(&n)
	return n, err
}

func (db *DB) CleanStalePeers(timeout time.Duration) ([]string, error) {
	cutoff := time.Now().UTC().Add(-timeout).Format(time.RFC3339)
	rows, err := db.conn.Query(`SELECT id, pid FROM peers WHERE last_seen < ?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stale []string
	for rows.Next() {
		var id string
		var pid int
		if err := rows.Scan(&id, &pid); err != nil {
			continue
		}
		// Check if process is still alive
		proc, err := os.FindProcess(pid)
		if err != nil || !isProcessAlive(proc) {
			stale = append(stale, id)
		}
	}
	for _, id := range stale {
		db.DeletePeer(id)
	}
	return stale, nil
}

// --- Message operations ---

func (db *DB) InsertMessage(m *types.Message) (int64, error) {
	var replyTo sql.NullInt64
	if m.ReplyTo != nil {
		replyTo = sql.NullInt64{Int64: *m.ReplyTo, Valid: true}
	}
	res, err := db.conn.Exec(
		`INSERT INTO messages (type, from_id, to_id, thread_id, reply_to, text, urgency, context, scope, sent_at, delivered)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(m.Type), m.FromID, nullStr(m.ToID), nullStr(m.ThreadID), replyTo,
		m.Text, string(m.Urgency), nullJSON(m.Context), nullStr(m.Scope),
		m.SentAt, boolToInt(m.Delivered),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (db *DB) MarkDelivered(id int64) error {
	_, err := db.conn.Exec(`UPDATE messages SET delivered = 1 WHERE id = ?`, id)
	return err
}

func (db *DB) UndeliveredMessages(peerID string) ([]types.Message, error) {
	rows, err := db.conn.Query(
		`SELECT id, type, from_id, to_id, thread_id, reply_to, text, urgency, context, scope, sent_at, delivered
		 FROM messages WHERE to_id = ? AND delivered = 0 ORDER BY id`, peerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []types.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, *m)
	}
	return msgs, rows.Err()
}

// --- Context operations ---

func (db *DB) SetContext(key, scopeType, scopeValue, value, setBy string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.conn.Exec(
		`INSERT INTO context (key, scope_type, scope_value, value, set_by, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(key, scope_type, scope_value) DO UPDATE SET value = ?, set_by = ?, updated_at = ?`,
		key, scopeType, scopeValue, value, setBy, now,
		value, setBy, now,
	)
	return err
}

func (db *DB) GetContext(key, scopeType, scopeValue string) (*types.ContextEntry, error) {
	row := db.conn.QueryRow(
		`SELECT key, scope_type, scope_value, value, set_by, updated_at
		 FROM context WHERE key = ? AND scope_type = ? AND scope_value = ?`,
		key, scopeType, scopeValue)
	var e types.ContextEntry
	err := row.Scan(&e.Key, &e.ScopeType, &e.ScopeValue, &e.Value, &e.SetBy, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (db *DB) ListContext(scopeType, scopeValue string) ([]types.ContextEntry, error) {
	var rows *sql.Rows
	var err error
	if scopeType != "" && scopeValue != "" {
		rows, err = db.conn.Query(
			`SELECT key, scope_type, scope_value, value, set_by, updated_at
			 FROM context WHERE scope_type = ? AND scope_value = ?`, scopeType, scopeValue)
	} else {
		rows, err = db.conn.Query(
			`SELECT key, scope_type, scope_value, value, set_by, updated_at FROM context`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []types.ContextEntry
	for rows.Next() {
		var e types.ContextEntry
		if err := rows.Scan(&e.Key, &e.ScopeType, &e.ScopeValue, &e.Value, &e.SetBy, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- helpers ---

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func nullJSON(data json.RawMessage) sql.NullString {
	if len(data) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: string(data), Valid: true}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanPeer(row scannable) (*types.Peer, error) {
	var p types.Peer
	var gitRoot, gitBranch, tty sql.NullString
	var afJSON string
	err := row.Scan(&p.ID, &p.PID, &p.CWD, &gitRoot, &gitBranch, &tty,
		&p.Summary, &afJSON, &p.RegisteredAt, &p.LastSeen)
	if err != nil {
		return nil, err
	}
	p.GitRoot = gitRoot.String
	p.GitBranch = gitBranch.String
	p.TTY = tty.String
	_ = json.Unmarshal([]byte(afJSON), &p.ActiveFiles)
	if p.ActiveFiles == nil {
		p.ActiveFiles = []string{}
	}
	return &p, nil
}

func scanPeerRows(rows *sql.Rows) (*types.Peer, error) {
	return scanPeer(rows)
}

func scanMessage(rows *sql.Rows) (*types.Message, error) {
	var m types.Message
	var toID, threadID, ctxJSON, scope sql.NullString
	var replyTo sql.NullInt64
	var delivered int
	err := rows.Scan(&m.ID, &m.Type, &m.FromID, &toID, &threadID, &replyTo,
		&m.Text, &m.Urgency, &ctxJSON, &scope, &m.SentAt, &delivered)
	if err != nil {
		return nil, err
	}
	m.ToID = toID.String
	m.ThreadID = threadID.String
	m.Scope = scope.String
	m.Delivered = delivered == 1
	if replyTo.Valid {
		m.ReplyTo = &replyTo.Int64
	}
	if ctxJSON.Valid {
		m.Context = json.RawMessage(ctxJSON.String)
	}
	return &m, nil
}

func isProcessAlive(proc *os.Process) bool {
	err := proc.Signal(os.Signal(nil))
	return err == nil
}
