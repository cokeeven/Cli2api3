package providers

import (
	"context"
	"sync"
)

// AttemptCredentialEvidence contains no credential material. It identifies
// the stored generation and account event sequence actually used by one
// provider attempt. Valid=false after a mismatch deliberately fails closed.
type AttemptCredentialEvidence struct {
	Generation    int64
	Version       uint64
	NeedsRecovery bool
	Observed      bool
	Valid         bool
}

type credentialEvidenceKey struct{}
type credentialEvidenceTrace struct {
	mu        sync.Mutex
	accountID string
	evidence  AttemptCredentialEvidence
	frozen    bool
}

func WithCredentialEvidence(ctx context.Context, accountID string) context.Context {
	return context.WithValue(ctx, credentialEvidenceKey{}, &credentialEvidenceTrace{accountID: accountID})
}

func RecordCredentialEvidence(ctx context.Context, accountID string, evidence AttemptCredentialEvidence) {
	trace, _ := ctx.Value(credentialEvidenceKey{}).(*credentialEvidenceTrace)
	if trace == nil || trace.accountID != accountID {
		return
	}
	trace.mu.Lock()
	if !trace.frozen {
		trace.evidence = evidence
	}
	trace.mu.Unlock()
}

func CredentialEvidence(ctx context.Context, accountID string) AttemptCredentialEvidence {
	trace, _ := ctx.Value(credentialEvidenceKey{}).(*credentialEvidenceTrace)
	if trace == nil || trace.accountID != accountID {
		return AttemptCredentialEvidence{}
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	return trace.evidence
}

// LoadCredentialPayloadWithEvidence reads the payload and its generation in
// one account-guarded operation. Comparing bytes later is insufficient: a
// newer login may write the same payload while advancing the generation.
func LoadCredentialPayloadWithEvidence(ctx context.Context, store interface {
	LoadCredentialPayload(context.Context, string) (string, []byte, error)
}, accountID string) (string, []byte, error) {
	trace, _ := ctx.Value(credentialEvidenceKey{}).(*credentialEvidenceTrace)
	if trace == nil || trace.accountID != accountID {
		return store.LoadCredentialPayload(ctx, accountID)
	}
	source, ok := store.(interface {
		LoadCredentialPayloadWithEvidence(context.Context, string) (string, []byte, error)
	})
	if !ok {
		RecordCredentialEvidence(ctx, accountID, AttemptCredentialEvidence{Observed: true})
		return store.LoadCredentialPayload(ctx, accountID)
	}
	return source.LoadCredentialPayloadWithEvidence(ctx, accountID)
}

// FreezeCredentialEvidence prevents catalog and other nested calls made
// during the same request from replacing the credential actually used in chat.
func FreezeCredentialEvidence(ctx context.Context, accountID string) {
	trace, _ := ctx.Value(credentialEvidenceKey{}).(*credentialEvidenceTrace)
	if trace == nil || trace.accountID != accountID {
		return
	}
	trace.mu.Lock()
	trace.frozen = true
	trace.mu.Unlock()
}

// EnsureCredentialEvidence supplies an attempt-local trace for direct adapter
// calls while retaining a trace already installed by the executor.
func EnsureCredentialEvidence(ctx context.Context, accountID string) context.Context {
	if trace, ok := ctx.Value(credentialEvidenceKey{}).(*credentialEvidenceTrace); ok && trace.accountID == accountID {
		return ctx
	}
	return WithCredentialEvidence(ctx, accountID)
}
