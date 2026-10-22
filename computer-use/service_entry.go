package main

// callForCommandLine returns exactly what an MCP client without structured
// content receives, except that images are files: their paths replace the
// base64 data.
func (s *service) callForCommandLine(name string, args map[string]any) (toolCallResult, error) {
	result, _, err := s.callForText(name, args)
	return result, err
}

// callForText is callForCommandLine plus the public error code of the
// result ("" on success), for entry points that map it to an exit status.
func (s *service) callForText(name string, args map[string]any) (toolCallResult, string, error) {
	result := prepareModelEvidence(name, s.callTool(name, args))
	code := ""
	if result.IsError {
		code = recordFromEvidence(name, result).Code
	}
	result, err := writeImagesToFiles(placeModelEvidence(name, result, false))
	return result, code, err
}
