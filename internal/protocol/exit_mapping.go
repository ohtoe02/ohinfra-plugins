package protocol

// resultExitCode is the canonical host-facing Result mapping. Serve deliberately
// does not apply it: a successfully transported protocol Result exits zero and
// the host maps the validated Result to its operator-facing exit code.
func resultExitCode(status Status, kind ErrorKind) int {
	switch status {
	case StatusPass, StatusInfo:
		return ExitOK
	case StatusWarning:
		return ExitWarning
	case StatusCritical:
		return ExitCritical
	case StatusCancelled:
		return ExitCancelled
	case StatusPartial:
		return ExitPartial
	case StatusError, StatusSkipped:
		return errorKindExitCode(kind)
	default:
		return ExitGeneral
	}
}

func errorKindExitCode(kind ErrorKind) int {
	switch kind {
	case ErrorArguments:
		return ExitArguments
	case ErrorPrivilege:
		return ExitPrivilege
	case ErrorDependency:
		return ExitDependency
	case ErrorCancelled:
		return ExitCancelled
	case ErrorTimeout:
		return ExitTimeout
	case ErrorConfiguration:
		return ExitConfiguration
	default:
		return ExitGeneral
	}
}

func processFailureKind(exitCode int) ErrorKind {
	switch exitCode {
	case ExitArguments:
		return ErrorArguments
	case ExitPrivilege:
		return ErrorPrivilege
	case ExitDependency:
		return ErrorDependency
	case ExitCancelled:
		return ErrorCancelled
	case ExitTimeout:
		return ErrorTimeout
	case ExitConfiguration:
		return ErrorConfiguration
	default:
		return ErrorGeneral
	}
}
