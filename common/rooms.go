package common

// MaxRoomTicketLen bounds one helper pairing ticket. A real ticket is about
// 215 characters; anything longer is refused before it is decoded.
const MaxRoomTicketLen = 512
