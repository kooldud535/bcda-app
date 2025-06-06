package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/CMSgov/bcda-app/bcda/models"
	"github.com/CMSgov/bcda-app/bcdaworker/repository"
	"github.com/huandu/go-sqlbuilder"
	"github.com/pborman/uuid"
)

type queryable interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

type executable interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

const (
	sqlFlavor = sqlbuilder.PostgreSQL
)

// Ensure Repository satisfies the interface
var _ repository.Repository = &Repository{}

type Repository struct {
	queryable
	executable
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db, db}
}

func NewRepositoryTx(tx *sql.Tx) *Repository {
	return &Repository{tx, tx}
}

func (r *Repository) GetACOByUUID(ctx context.Context, uuid uuid.UUID) (*models.ACO, error) {
	sb := sqlFlavor.NewSelectBuilder().Select("id", "uuid", "cms_id", "name").From("acos")
	sb.Where(sb.Equal("uuid", uuid))

	query, args := sb.Build()
	row := r.QueryRowContext(ctx, query, args...)
	var (
		aco         models.ACO
		name, cmsID sql.NullString
	)
	err := row.Scan(&aco.ID, &aco.UUID, &cmsID, &name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no ACO record found for uuid %s", uuid)
		}
		return nil, err
	}
	aco.Name, aco.CMSID = name.String, &cmsID.String
	return &aco, nil
}

func (r *Repository) GetCCLFBeneficiaryByID(ctx context.Context, id uint) (*models.CCLFBeneficiary, error) {
	sb := sqlFlavor.NewSelectBuilder()
	sb.Select("id", "file_id", "mbi", "blue_button_id")
	sb.From("cclf_beneficiaries").Where(sb.Equal("id", id))

	query, args := sb.Build()
	row := r.QueryRowContext(ctx, query, args...)

	var (
		bene models.CCLFBeneficiary
		bbID sql.NullString
	)

	if err := row.Scan(&bene.ID, &bene.FileID, &bene.MBI, &bbID); err != nil {
		return nil, err
	}
	bene.BlueButtonID = bbID.String

	return &bene, nil
}

func (r *Repository) GetJobByID(ctx context.Context, jobID uint) (*models.Job, error) {
	sb := sqlFlavor.NewSelectBuilder()
	sb.Select("id", "aco_id", "request_url", "status", "transaction_time", "job_count", "created_at", "updated_at")
	sb.From("jobs").Where(sb.Equal("id", jobID))

	query, args := sb.Build()

	var (
		j                                     models.Job
		transactionTime, createdAt, updatedAt sql.NullTime
	)

	err := r.QueryRowContext(ctx, query, args...).Scan(&j.ID, &j.ACOID, &j.RequestURL, &j.Status, &transactionTime,
		&j.JobCount, &createdAt, &updatedAt)
	j.TransactionTime, j.CreatedAt, j.UpdatedAt = transactionTime.Time, createdAt.Time, updatedAt.Time

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrJobNotFound
		}
		return nil, err
	}

	return &j, nil
}

func (r *Repository) UpdateJobStatus(ctx context.Context, jobID uint, new models.JobStatus) error {
	return r.updateJob(ctx,
		map[string]interface{}{"id": jobID},
		map[string]interface{}{"status": new})
}

func (r *Repository) UpdateJobStatusCheckStatus(ctx context.Context, jobID uint, current, new models.JobStatus) error {
	return r.updateJob(ctx,
		map[string]interface{}{"id": jobID, "status": current},
		map[string]interface{}{"status": new})
}

func (r *Repository) CreateJobKey(ctx context.Context, jobKey models.JobKey) error {
	ib := sqlFlavor.NewInsertBuilder().InsertInto("job_keys")
	ib.Cols("job_id", "que_job_id", "file_name", "resource_type").
		Values(jobKey.JobID, jobKey.QueJobID, jobKey.FileName, jobKey.ResourceType)

	query, args := ib.Build()
	_, err := r.ExecContext(ctx, query, args...)
	return err
}

func (r *Repository) CreateJobKeys(ctx context.Context, jobKeys []models.JobKey) error {
	ib := sqlFlavor.NewInsertBuilder().InsertInto("job_keys")
	ib.Cols("job_id", "que_job_id", "file_name", "resource_type")

	for _, jobKey := range jobKeys {
		ib.Values(jobKey.JobID, jobKey.QueJobID, jobKey.FileName, jobKey.ResourceType)
	}

	query, args := ib.Build()
	_, err := r.ExecContext(ctx, query, args...)
	return err
}

func (r *Repository) GetJobKeyCount(ctx context.Context, jobID uint) (int, error) {
	sb := sqlFlavor.NewSelectBuilder().Select("COUNT(1)").From("job_keys")
	sb.Where(sb.Equal("job_id", jobID))
	sb.Where(sb.NotLike("file_name", "%-error.ndjson%")) // Ignore error files from completed count.

	query, args := sb.Build()
	var count int
	if err := r.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return -1, err
	}
	return count, nil
}

func (r *Repository) GetUniqueJobKeyCount(ctx context.Context, jobID uint) (int, error) {
	sb := sqlFlavor.NewSelectBuilder().Select("COUNT(DISTINCT que_job_id)").From("job_keys")
	sb.Where(sb.Equal("job_id", jobID))

	query, args := sb.Build()
	var count int
	if err := r.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return -1, err
	}
	return count, nil
}

func (r *Repository) GetJobKey(ctx context.Context, jobID uint, qjobID int64) (*models.JobKey, error) {
	sb := sqlFlavor.NewSelectBuilder().Select("id").From("job_keys")
	sb.Where(sb.And(sb.Equal("job_id", jobID), sb.Equal("que_job_id", qjobID)))

	query, args := sb.Build()
	row := r.QueryRowContext(ctx, query, args...)

	jk := &models.JobKey{JobID: jobID, QueJobID: &qjobID}

	if err := row.Scan(&jk.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, repository.ErrJobKeyNotFound
		}
		return nil, err
	}

	return jk, nil
}

func (r *Repository) updateJob(ctx context.Context, clauses map[string]interface{}, fieldAndValues map[string]interface{}) error {
	ub := sqlFlavor.NewUpdateBuilder().Update("jobs")
	ub.Set(ub.Assign("updated_at", sqlbuilder.Raw("NOW()")))
	for field, value := range fieldAndValues {
		ub.SetMore(ub.Assign(field, value))
	}
	for field, value := range clauses {
		ub.Where(ub.Equal(field, value))
	}

	query, args := ub.Build()
	result, err := r.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if affected == 0 {
		return repository.ErrJobNotUpdated
	}

	return nil
}
