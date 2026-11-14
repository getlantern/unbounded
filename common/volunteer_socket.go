package common

import "net/url"

// A volunteer can hold one WebSocket to Freddie, at VolunteerSocketEndpoint, and send over it the
// signaling messages it would otherwise POST to the signaling endpoint one HTTP request at a time.
// Freddie handles each exactly as it handles the POST, so the consumer on the other end sees no
// difference, and replies on the same socket. Every message is one JSON text frame.
//
// A volunteer whose socket fails falls back to POSTing, so a Freddie or network path without the
// endpoint costs nothing but the attempt.

// VolunteerSocketEndpoint is the path of Freddie's volunteer WebSocket.
const VolunteerSocketEndpoint = "/v1/volunteer"

// VolunteerRequest is one signaling message over the volunteer socket: the form fields its POST
// would carry. ID is the volunteer's own, echoed in the reply, so requests can overlap.
type VolunteerRequest struct {
	ID   uint64     `json:"id"`
	Form url.Values `json:"form"`
}

// VolunteerReply is Freddie's reply to the VolunteerRequest with the same ID: the status its POST
// would have got, and the response body, "" when the partner didn't reply in time.
type VolunteerReply struct {
	ID     uint64 `json:"id"`
	Status int    `json:"status"`
	Body   string `json:"body,omitempty"`
}
