package models

import (
	"time"

	"github.com/google/uuid"
)

// SegmentBooking registra a presença confirmada de um passageiro (local ou remoto)
// em um trecho operado por esta empresa, garantindo rastreabilidade entre nós.
type SegmentBooking struct {
	ID             uuid.UUID `json:"id"`
	TxID           string    `json:"tx_id,omitempty"`
	ReservationID  uuid.UUID `json:"reservation_id,omitempty"`
	RideID         uuid.UUID `json:"ride_id"`
	SegmentID      uuid.UUID `json:"segment_id"`
	PassengerID    uuid.UUID `json:"passenger_id"`
	PassengerName  string    `json:"passenger_name"`
	PassengerEmail string    `json:"passenger_email,omitempty"`
	CompanyID      string    `json:"company_id"`
	NoShow         bool      `json:"no_show,omitempty"`
	Cancelled      bool      `json:"cancelled,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}
