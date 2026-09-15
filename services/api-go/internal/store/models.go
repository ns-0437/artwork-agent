package store

import "time"

type Order struct {
	ID                string
	OwnerID           string
	ProductType       string
	DeclaredWidth     float64
	DeclaredHeight    float64
	DeclaredUnit      string
	CustomerRequest   *string
	ArtworkVersion    int
	Intent            *string
	ArtworkIsTrimOnly *bool
	CurrentAssetID    *string
	TrimXPx           *float64
	TrimYPx           *float64
	TrimWidthPx       *float64
	TrimHeightPx      *float64
	CaseVersion       int
	ArtworkStatus     string
	ProofStatus       string
	ProductionStatus  string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Asset struct {
	ID          string
	OrderID     string
	Kind        string
	StorageKey  string
	SHA256      string
	ContentType string
	WidthPx     *int
	HeightPx    *int
	CreatedAt   time.Time
}

type Job struct {
	ID               string
	OrderID          string
	JobType          string
	Status           string
	WorkerID         *string
	LeaseExpiresAt   *time.Time
	AttemptCount     int
	LastError        *string
	InputAssetID     *string
	InputCaseVersion int
	IdempotencyKey   *string // set for repair jobs; nil for inspect jobs
	Result           *string // JSON-encoded result (e.g. inspection dimensions/mode/format), nil until completion
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Finding struct {
	ID          string
	OrderID     string
	JobID       *string
	CheckName   string
	Result      string
	Evidence    string // JSON-encoded evidence blob
	RuleVersion string
	CreatedAt   time.Time
}

type Repair struct {
	ID             string
	OrderID        string
	IdempotencyKey string
	Status         string // PENDING | REPAIRED | REJECTED
	Reason         *string
	SourceAssetID  *string
	DerivedAssetID *string
	SourceHash     *string
	DerivedHash    *string
	Diagnosis      *string // JSON-encoded
	JobID          *string
	CreatedAt      time.Time
}
