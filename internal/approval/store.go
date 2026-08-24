package approval

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"webring/internal/models"
	"webring/internal/requests"
	"webring/internal/telegram"

	"github.com/lib/pq"
)

// pgStore is the PostgreSQL implementation of Store.
type pgStore struct {
	db *sql.DB
}

// NewStore returns a Store backed by the application database.
func NewStore(db *sql.DB) Store {
	return &pgStore{db: db}
}

const adminFilter = `is_admin = true AND telegram_id IS NOT NULL`

func (s *pgStore) AdminCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE `+adminFilter).Scan(&count)
	return count, err
}

func (s *pgStore) AdminTelegramIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT telegram_id FROM users WHERE `+adminFilter)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	var ids []int64
	for rows.Next() {
		var id int64
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *pgStore) AdminByTelegramID(ctx context.Context, telegramID int64) (*models.User, error) {
	var user models.User
	var username, firstName, lastName sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, telegram_id, telegram_username, first_name, last_name
		FROM users
		WHERE telegram_id = $1 AND is_admin = true
	`, telegramID).Scan(&user.ID, &user.TelegramID, &username, &firstName, &lastName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	user.IsAdmin = true
	user.TelegramUsername = nullable(username)
	user.FirstName = nullable(firstName)
	user.LastName = nullable(lastName)
	return &user, nil
}

func (s *pgStore) CreatePoll(ctx context.Context, poll Poll) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO request_polls (poll_id, request_id, chat_id, message_id, threshold, status)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, poll.PollID, poll.RequestID, poll.ChatID, poll.MessageID, poll.Threshold, string(poll.Status))
	return err
}

const pollColumns = `poll_id, request_id, chat_id, message_id, threshold, status`

func scanPoll(row *sql.Row) (*Poll, error) {
	var poll Poll
	var requestID sql.NullInt64
	var status string

	err := row.Scan(&poll.PollID, &requestID, &poll.ChatID, &poll.MessageID, &poll.Threshold, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	// request_id is cleared when the request row is deleted, which is expected once a
	// request has been applied.
	if requestID.Valid {
		poll.RequestID = int(requestID.Int64)
	}
	poll.Status = Status(status)
	return &poll, nil
}

func (s *pgStore) PollByID(ctx context.Context, pollID string) (*Poll, error) {
	return scanPoll(s.db.QueryRowContext(ctx,
		`SELECT `+pollColumns+` FROM request_polls WHERE poll_id = $1`, pollID))
}

func (s *pgStore) PollByRequestID(ctx context.Context, requestID int) (*Poll, error) {
	return scanPoll(s.db.QueryRowContext(ctx,
		`SELECT `+pollColumns+` FROM request_polls
		 WHERE request_id = $1 ORDER BY created_at DESC LIMIT 1`, requestID))
}

func (s *pgStore) SetStatus(ctx context.Context, pollID string, status Status) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE request_polls
		SET status = $2, closed_at = CASE WHEN $2 = 'open' THEN NULL ELSE NOW() END
		WHERE poll_id = $1
	`, pollID, string(status))
	return err
}

// ClaimDecision flips an undecided poll to a final status in a single statement, so only
// one caller can ever win. A canceled poll is still claimable: canceling is how a
// failed apply hands the request back to the dashboard.
func (s *pgStore) ClaimDecision(ctx context.Context, requestID int, status Status) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE request_polls SET status = $2, closed_at = NOW()
		WHERE request_id = $1 AND status IN ('open', 'canceled')
	`, requestID, string(status))
	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected > 0 {
		return true, nil
	}

	// Nothing was claimable. Either the request never had a poll — polls may be
	// disabled, or sending one failed — in which case the caller is free to proceed, or
	// a poll exists and has already decided it.
	var exists bool
	err = s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM request_polls WHERE request_id = $1)`, requestID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

func (s *pgStore) RecordVote(ctx context.Context, pollID string, userID int,
	telegramID int64, optionID int) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO request_poll_votes (poll_id, user_id, telegram_id, option_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (poll_id, telegram_id)
		DO UPDATE SET option_id = EXCLUDED.option_id, user_id = EXCLUDED.user_id, voted_at = NOW()
	`, pollID, userID, telegramID, optionID)
	return err
}

func (s *pgStore) DeleteVote(ctx context.Context, pollID string, telegramID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM request_poll_votes WHERE poll_id = $1 AND telegram_id = $2`, pollID, telegramID)
	return err
}

// Tally counts votes per option, ignoring anyone who is no longer an admin. Re-checking
// here means a demoted admin's vote stops counting even though it was valid when cast.
func (s *pgStore) Tally(ctx context.Context, pollID string) (map[int]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.option_id, COUNT(*)
		FROM request_poll_votes v
		JOIN users u ON v.user_id = u.id
		WHERE v.poll_id = $1 AND u.is_admin = true
		GROUP BY v.option_id
	`, pollID)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	tally := make(map[int]int)
	for rows.Next() {
		var option, count int
		if scanErr := rows.Scan(&option, &count); scanErr != nil {
			return nil, scanErr
		}
		tally[option] = count
	}
	return tally, rows.Err()
}

func (s *pgStore) VoterNames(ctx context.Context, pollID string, optionID int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.telegram_username, u.first_name, u.last_name
		FROM request_poll_votes v
		JOIN users u ON v.user_id = u.id
		WHERE v.poll_id = $1 AND v.option_id = $2 AND u.is_admin = true
		ORDER BY v.voted_at
	`, pollID, optionID)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	var names []string
	for rows.Next() {
		var username, firstName, lastName sql.NullString
		if scanErr := rows.Scan(&username, &firstName, &lastName); scanErr != nil {
			return nil, scanErr
		}
		names = append(names, telegram.ResolveUserName(&models.User{
			TelegramUsername: nullable(username),
			FirstName:        nullable(firstName),
			LastName:         nullable(lastName),
		}))
	}
	return names, rows.Err()
}

// VoteSummaries loads every poll and its votes in one query, so rendering a dashboard
// full of requests does not turn into a query per card.
func (s *pgStore) VoteSummaries(ctx context.Context, requestIDs []int) (map[int]*VoteSummary, error) {
	summaries := make(map[int]*VoteSummary, len(requestIDs))
	if len(requestIDs) == 0 {
		return summaries, nil
	}

	ids := make([]int64, len(requestIDs))
	for i, id := range requestIDs {
		ids[i] = int64(id)
	}

	// The joins are outer so that a poll with no votes yet still produces a row, and a
	// vote by someone who has since lost admin rights produces a row with no user, which
	// is skipped below.
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.request_id, p.threshold, p.status,
		       v.option_id, u.id, u.telegram_username, u.first_name, u.last_name
		FROM request_polls p
		LEFT JOIN request_poll_votes v ON v.poll_id = p.poll_id
		LEFT JOIN users u ON u.id = v.user_id AND u.is_admin = true
		WHERE p.request_id = ANY($1)
		ORDER BY p.request_id, v.voted_at
	`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)

	for rows.Next() {
		var requestID, threshold int
		var status string
		var optionID, voterID sql.NullInt64
		var username, firstName, lastName sql.NullString

		if scanErr := rows.Scan(&requestID, &threshold, &status,
			&optionID, &voterID, &username, &firstName, &lastName); scanErr != nil {
			return nil, scanErr
		}

		summary, ok := summaries[requestID]
		if !ok {
			summary = &VoteSummary{Threshold: threshold, Status: Status(status)}
			summaries[requestID] = summary
		}

		if !optionID.Valid || !voterID.Valid {
			continue
		}

		voter := Voter{Name: telegram.ResolveUserName(&models.User{
			TelegramUsername: nullable(username),
			FirstName:        nullable(firstName),
			LastName:         nullable(lastName),
		})}
		// The handle is only worth showing next to a real name; otherwise it is the name.
		if firstName.Valid && firstName.String != "" && username.Valid {
			voter.Username = username.String
		}

		if int(optionID.Int64) == telegram.OptionApprove {
			summary.Approve = append(summary.Approve, voter)
		} else {
			summary.Decline = append(summary.Decline, voter)
		}
	}

	return summaries, rows.Err()
}

func nullable(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	s := value.String
	return &s
}

func closeRows(rows *sql.Rows) {
	if err := rows.Close(); err != nil {
		log.Printf("Error closing rows: %v", err)
	}
}

// dbDecider applies vote outcomes through the requests package.
type dbDecider struct {
	db *sql.DB
}

// NewDecider returns a Decider backed by the application database.
func NewDecider(db *sql.DB) Decider {
	return &dbDecider{db: db}
}

func (d *dbDecider) Load(requestID int) (*models.UpdateRequest, error) {
	req, err := requests.Load(d.db, requestID)
	if errors.Is(err, requests.ErrNotFound) {
		return nil, nil
	}
	return req, err
}

func (d *dbDecider) Approve(req *models.UpdateRequest) error {
	return requests.Approve(d.db, req)
}

func (d *dbDecider) Decline(req *models.UpdateRequest) error {
	return requests.Decline(d.db, req)
}
