package failpoint

const ExitCode = 137

const (
	ConsumerAfterCommit                = "consumer.after_commit"
	OutboxAfterClaim                   = "outbox.after_claim"
	OutboxAfterPublish                 = "outbox.after_publish"
	ResolverAfterReschedule            = "resolver.after_reschedule"
	UsecaseAfterPendingReferenceCommit = "usecase.after_pending_reference_commit"
)
