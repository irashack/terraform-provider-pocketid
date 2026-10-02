package client

import "time"

// SetRetryPolicyForTest shortens c's retry limits so a test can exercise them
// in milliseconds.
func SetRetryPolicyForTest(c *Client, maxAttempts int, backoffUnit, maxWait, maxElapsed time.Duration) {
	c.retry = retryPolicy{
		maxAttempts: maxAttempts,
		backoffUnit: backoffUnit,
		maxWait:     maxWait,
		maxElapsed:  maxElapsed,
	}
}
