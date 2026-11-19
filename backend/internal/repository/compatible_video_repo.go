package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.ReplicateMusicRepository = (*usageBillingRepository)(nil)

func (r *usageBillingRepository) CreateReplicateMusic(ctx context.Context, task *service.ReplicateMusicTask) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockAccountForMediaTask(ctx, tx, task.AccountID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO replicate_music_tasks(id,user_id,api_key_id,group_id,account_id,hold_amount,payload) VALUES($1,$2,$3,$4,$5,$6,$7)`, task.ID, task.UserID, task.APIKeyID, task.GroupID, task.AccountID, task.HoldAmount, payload)
	if err != nil {
		return err
	}
	if task.HoldAmount > 0 {
		result, e := tx.ExecContext(ctx, `UPDATE users SET balance=balance-$1,frozen_balance=COALESCE(frozen_balance,0)+$1,updated_at=now() WHERE id=$2 AND deleted_at IS NULL AND balance >= $1`, task.HoldAmount, task.UserID)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return service.ErrInsufficientBalance
		}
	}
	return tx.Commit()
}

func scanReplicateMusic(row interface{ Scan(...any) error }) (*service.ReplicateMusicTask, error) {
	var payload []byte
	var output []byte
	var id, upstream, status string
	var settled bool
	if err := row.Scan(&id, &upstream, &status, &settled, &output, &payload); err != nil {
		return nil, err
	}
	var task service.ReplicateMusicTask
	if err := json.Unmarshal(payload, &task); err != nil {
		return nil, err
	}
	task.ID, task.UpstreamID, task.Status, task.Settled = id, upstream, status, settled
	task.Output = output
	return &task, nil
}
func (r *usageBillingRepository) GetReplicateMusic(ctx context.Context, id string) (*service.ReplicateMusicTask, error) {
	return scanReplicateMusic(r.db.QueryRowContext(ctx, `SELECT id,upstream_id,status,settled,output,payload FROM replicate_music_tasks WHERE id=$1`, id))
}
func (r *usageBillingRepository) AcceptReplicateMusic(ctx context.Context, id, upstream string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE replicate_music_tasks SET upstream_id=$2,status='starting',updated_at=now() WHERE id=$1 AND NOT settled AND (upstream_id='' OR upstream_id=$2)`, id, upstream)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrCompatibleVideoNotFound
	}
	return nil
}

func (r *usageBillingRepository) MarkReplicateMusicSubmissionUnknown(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE replicate_music_tasks SET status='submission_unknown',updated_at=now() WHERE id=$1 AND NOT settled`, id)
	return err
}
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

func (r *usageBillingRepository) ListPendingReplicateMusic(ctx context.Context, limit int) ([]*service.ReplicateMusicTask, error) {
	rows, err := r.db.QueryContext(ctx, `WITH pending AS (SELECT id FROM replicate_music_tasks WHERE NOT settled AND (upstream_id<>'' OR (status IN ('submitting','submission_unknown') AND updated_at < now() - interval '2 minutes')) ORDER BY updated_at LIMIT $1 FOR UPDATE SKIP LOCKED)
 UPDATE replicate_music_tasks t SET updated_at=now() FROM pending p WHERE t.id=p.id RETURNING t.id,t.upstream_id,t.status,t.settled,t.output,t.payload`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []*service.ReplicateMusicTask
	for rows.Next() {
		task, scanErr := scanReplicateMusic(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *usageBillingRepository) ReleaseReplicateMusic(ctx context.Context, id, status string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID int64
	var hold float64
	err = tx.QueryRowContext(ctx, `UPDATE replicate_music_tasks SET status=$2,settled=true,updated_at=now() WHERE id=$1 AND NOT settled RETURNING user_id,hold_amount`, id, status).Scan(&userID, &hold)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = releaseCompatibleVideoHold(ctx, tx, userID, hold); err != nil {
		return err
	}
	return tx.Commit()
}

var _ service.CompatibleVideoRepository = (*usageBillingRepository)(nil)

func (r *usageBillingRepository) CreateCompatibleVideo(ctx context.Context, task *service.CompatibleVideoTask) error {
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockAccountForMediaTask(ctx, tx, task.AccountID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO compatible_video_tasks(id,user_id,api_key_id,group_id,account_id,hold_amount,payload) VALUES($1,$2,$3,$4,$5,$6,$7)`, task.ID, task.UserID, task.APIKeyID, task.GroupID, task.AccountID, task.HoldAmount, payload)
	if err != nil {
		return err
	}
	if task.HoldAmount > 0 {
		result, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance-$1,frozen_balance=COALESCE(frozen_balance,0)+$1,updated_at=now() WHERE id=$2 AND deleted_at IS NULL AND balance >= $1`, task.HoldAmount, task.UserID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return service.ErrInsufficientBalance
		}
	}
	return tx.Commit()
}

func lockAccountForMediaTask(ctx context.Context, tx *sql.Tx, accountID int64) error {
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, accountID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrAccountNotFound
		}
		return err
	}
	return nil
}

func scanCompatibleVideo(row interface{ Scan(...any) error }) (*service.CompatibleVideoTask, error) {
	var payload []byte
	var id, upstream, status string
	var settled bool
	err := row.Scan(&id, &upstream, &status, &settled, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCompatibleVideoNotFound
	}
	if err != nil {
		return nil, err
	}
	var task service.CompatibleVideoTask
	if err = json.Unmarshal(payload, &task); err != nil {
		return nil, err
	}
	task.ID, task.UpstreamID, task.Status, task.Settled = id, upstream, status, settled
	return &task, nil
}

func (r *usageBillingRepository) GetCompatibleVideo(ctx context.Context, id string) (*service.CompatibleVideoTask, error) {
	return scanCompatibleVideo(r.db.QueryRowContext(ctx, `SELECT id,upstream_id,status,settled,payload FROM compatible_video_tasks WHERE id=$1`, id))
}
func (r *usageBillingRepository) AcceptCompatibleVideo(ctx context.Context, id, upstream string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE compatible_video_tasks SET upstream_id=$2,status='queued',updated_at=now() WHERE id=$1 AND NOT settled AND (upstream_id='' OR upstream_id=$2)`, id, upstream)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrCompatibleVideoNotFound
	}
	return nil
}

func (r *usageBillingRepository) MarkCompatibleVideoSubmissionUnknown(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE compatible_video_tasks SET status='submission_unknown',updated_at=now() WHERE id=$1 AND NOT settled`, id)
	return err
}
func (r *usageBillingRepository) ReleaseCompatibleVideo(ctx context.Context, id, status string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID int64
	var hold float64
	err = tx.QueryRowContext(ctx, `UPDATE compatible_video_tasks SET status=$2,settled=true,updated_at=now() WHERE id=$1 AND NOT settled RETURNING user_id,hold_amount`, id, status).Scan(&userID, &hold)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = releaseCompatibleVideoHold(ctx, tx, userID, hold); err != nil {
		return err
	}
	return tx.Commit()
}
func releaseCompatibleVideoHold(ctx context.Context, tx *sql.Tx, userID int64, hold float64) error {
	if hold <= 0 {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance+$1,frozen_balance=frozen_balance-$1,updated_at=now() WHERE id=$2 AND frozen_balance >= $1`, hold, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("video balance hold missing for user %d", userID)
	}
	return nil
}
func (r *usageBillingRepository) ListPendingCompatibleVideos(ctx context.Context, limit int) ([]*service.CompatibleVideoTask, error) {
	// Rotate the scan cursor so slow/stuck jobs cannot starve newer completions.
	rows, err := r.db.QueryContext(ctx, `WITH pending AS (SELECT id FROM compatible_video_tasks WHERE NOT settled AND (upstream_id<>'' OR (status IN ('submitting','submission_unknown') AND updated_at < now() - interval '2 minutes')) ORDER BY updated_at LIMIT $1 FOR UPDATE SKIP LOCKED)
 UPDATE compatible_video_tasks t SET updated_at=now() FROM pending p WHERE t.id=p.id RETURNING t.id,t.upstream_id,t.status,t.settled,t.payload`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []*service.CompatibleVideoTask
	for rows.Next() {
		t, err := scanCompatibleVideo(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// Called inside the same transaction as usage_billing_dedup and all normal
// billing effects. A failed debit rolls back both capture and task settlement.
func captureCompatibleVideoTask(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	var hold float64
	var userID, keyID int64
	var settled bool
	var status string
	err := tx.QueryRowContext(ctx, `SELECT user_id,api_key_id,hold_amount,settled,status FROM compatible_video_tasks WHERE id=$1 FOR UPDATE`, cmd.CompatibleVideoTaskID).Scan(&userID, &keyID, &hold, &settled, &status)
	if err != nil {
		return err
	}
	if userID != cmd.UserID || keyID != cmd.APIKeyID || (settled && status != "completed") {
		return service.ErrUsageBillingRequestConflict
	}
	if settled {
		return service.ErrUsageBillingRequestConflict
	}
	if err = releaseCompatibleVideoHold(ctx, tx, userID, hold); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE compatible_video_tasks SET status='completed',settled=true,updated_at=now() WHERE id=$1`, cmd.CompatibleVideoTaskID)
	return err
}

func captureReplicateMusicTask(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	var userID, keyID int64
	var hold float64
	var settled bool
	if err := tx.QueryRowContext(ctx, `SELECT user_id,api_key_id,hold_amount,settled FROM replicate_music_tasks WHERE id=$1 FOR UPDATE`, cmd.ReplicateMusicTaskID).Scan(&userID, &keyID, &hold, &settled); err != nil {
		return err
	}
	if userID != cmd.UserID || keyID != cmd.APIKeyID || settled {
		return service.ErrUsageBillingRequestConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE replicate_music_tasks SET status='succeeded',settled=true,output=$2,updated_at=now() WHERE id=$1 AND NOT settled`, cmd.ReplicateMusicTaskID, nullableJSON(cmd.ReplicateMusicOutput))
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return service.ErrUsageBillingRequestConflict
	}
	if hold <= 0 {
		return nil
	}
	result, err = tx.ExecContext(ctx, `UPDATE users SET frozen_balance=COALESCE(frozen_balance,0)-$1,updated_at=now() WHERE id=$2 AND COALESCE(frozen_balance,0) >= $1`, hold, userID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("music balance hold missing for user %d", userID)
	}
	return nil
}
