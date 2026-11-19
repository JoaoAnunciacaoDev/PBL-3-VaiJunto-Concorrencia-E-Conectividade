package server

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/enum"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/google/uuid"
)

// SaveSegmentBooking registra de forma idempotente o vínculo de um passageiro (local ou remoto)
// em um trecho operado por este servidor.
func (r *Repository) SaveSegmentBooking(booking *models.SegmentBooking) error {
	if booking == nil || booking.RideID == uuid.Nil || booking.SegmentID == uuid.Nil || booking.PassengerID == uuid.Nil {
		return errors.New("dados de reserva do trecho inválidos")
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.bookingsMu.Lock()
	defer r.bookingsMu.Unlock()

	for _, existing := range r.bookings {
		sameTx := booking.TxID != "" && existing.TxID == booking.TxID && existing.SegmentID == booking.SegmentID
		sameRes := booking.ReservationID != uuid.Nil && existing.ReservationID == booking.ReservationID &&
			existing.SegmentID == booking.SegmentID && existing.PassengerID == booking.PassengerID
		if sameTx || sameRes {
			changed := false
			if existing.PassengerName == "" && booking.PassengerName != "" {
				existing.PassengerName = booking.PassengerName
				changed = true
			}
			if existing.PassengerEmail == "" && booking.PassengerEmail != "" {
				existing.PassengerEmail = booking.PassengerEmail
				changed = true
			}
			if existing.ReservationID == uuid.Nil && booking.ReservationID != uuid.Nil {
				existing.ReservationID = booking.ReservationID
				changed = true
			}
			if changed {
				return r.saveStateLocked()
			}
			return nil
		}
	}

	copyBooking := cloneSegmentBooking(booking)
	if copyBooking.ID == uuid.Nil {
		copyBooking.ID = uuid.New()
	}
	if copyBooking.CreatedAt.IsZero() {
		copyBooking.CreatedAt = time.Now()
	}

	r.bookings[copyBooking.ID] = copyBooking
	if err := r.saveStateLocked(); err != nil {
		delete(r.bookings, copyBooking.ID)
		return fmt.Errorf("salvar vínculo do passageiro no trecho: %w", err)
	}

	return nil
}

// CancelSegmentBooking marca como cancelados os vínculos de passageiros no trecho informado.
func (r *Repository) CancelSegmentBooking(segmentID, reservationID, passengerID uuid.UUID) error {
	if segmentID == uuid.Nil || (reservationID == uuid.Nil && passengerID == uuid.Nil) {
		return nil
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.bookingsMu.Lock()
	defer r.bookingsMu.Unlock()

	var modified []*models.SegmentBooking
	for _, b := range r.bookings {
		if b.Cancelled || b.SegmentID != segmentID {
			continue
		}
		if reservationID != uuid.Nil && b.ReservationID != uuid.Nil && b.ReservationID != reservationID {
			continue
		}
		if passengerID != uuid.Nil && b.PassengerID != passengerID {
			continue
		}
		b.Cancelled = true
		modified = append(modified, b)
	}

	if len(modified) == 0 {
		return nil
	}

	if err := r.saveStateLocked(); err != nil {
		for _, b := range modified {
			b.Cancelled = false
		}
		return fmt.Errorf("salvar cancelamento de vínculo de trecho: %w", err)
	}

	return nil
}

// MarkBookingNoShow valida que o passageiro possui reserva confirmada na carona e marca
// a ocorrência de ausência apenas na reserva/booking daquele passageiro, sem travar a carona inteira.
func (r *Repository) MarkBookingNoShow(rideID, passengerID uuid.UUID) (uuid.UUID, error) {
	if rideID == uuid.Nil || passengerID == uuid.Nil {
		return uuid.Nil, errors.New("carona e passageiro são obrigatórios")
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.reservationsMu.RLock()
	defer r.reservationsMu.RUnlock()

	r.bookingsMu.Lock()
	defer r.bookingsMu.Unlock()

	var modified []*models.SegmentBooking
	var foundReservationID uuid.UUID

	for _, b := range r.bookings {
		if !b.Cancelled && b.RideID == rideID && b.PassengerID == passengerID {
			if foundReservationID == uuid.Nil && b.ReservationID != uuid.Nil {
				foundReservationID = b.ReservationID
			}
			if !b.NoShow {
				b.NoShow = true
				modified = append(modified, b)
			}
		}
	}

	localFound := false
	for _, res := range r.reservations {
		if res.Status != enum.Confirmada || res.PassengerID != passengerID {
			continue
		}
		if reservationContainsRide(res, rideID) {
			localFound = true
			if foundReservationID == uuid.Nil {
				foundReservationID = res.ID
			}
			break
		}
	}

	if len(modified) == 0 && !localFound && foundReservationID == uuid.Nil {
		// Verifica se já existia algum booking ativo (mesmo já marcado anteriormente)
		hasAnyActive := false
		for _, b := range r.bookings {
			if !b.Cancelled && b.RideID == rideID && b.PassengerID == passengerID {
				hasAnyActive = true
				break
			}
		}
		if !hasAnyActive {
			return uuid.Nil, errors.New("passageiro não possui reserva confirmada nesta carona")
		}
	}

	if len(modified) > 0 {
		if err := r.saveStateLocked(); err != nil {
			for _, b := range modified {
				b.NoShow = false
			}
			return uuid.Nil, fmt.Errorf("salvar marcação de no-show: %w", err)
		}
	}

	return foundReservationID, nil
}

// HasConfirmedBookingForRide verifica se o passageiro possui reserva confirmada (local ou remota)
// para a carona informada, retornando o ID da reserva, os IDs dos trechos reservados e a empresa de origem.
func (r *Repository) HasConfirmedBookingForRide(rideID, passengerID uuid.UUID) (uuid.UUID, []uuid.UUID, string, bool) {
	if rideID == uuid.Nil || passengerID == uuid.Nil {
		return uuid.Nil, nil, "", false
	}

	r.reservationsMu.RLock()
	defer r.reservationsMu.RUnlock()

	r.bookingsMu.RLock()
	defer r.bookingsMu.RUnlock()

	for _, res := range r.reservations {
		if res.Status != enum.Confirmada || res.PassengerID != passengerID {
			continue
		}
		var segIDs []uuid.UUID
		for _, seg := range res.Segments {
			if seg.RideID == rideID {
				segIDs = append(segIDs, seg.SegmentID)
			}
		}
		if len(segIDs) > 0 {
			return res.ID, segIDs, "", true
		}
	}

	var resID uuid.UUID
	var segIDs []uuid.UUID
	var companyID string
	seenSeg := make(map[uuid.UUID]struct{})

	for _, b := range r.bookings {
		if b.Cancelled || b.RideID != rideID || b.PassengerID != passengerID {
			continue
		}
		if resID == uuid.Nil && b.ReservationID != uuid.Nil {
			resID = b.ReservationID
		}
		if companyID == "" && b.CompanyID != "" {
			companyID = b.CompanyID
		}
		if _, ok := seenSeg[b.SegmentID]; !ok {
			seenSeg[b.SegmentID] = struct{}{}
			segIDs = append(segIDs, b.SegmentID)
		}
	}

	if len(segIDs) > 0 {
		return resID, segIDs, companyID, true
	}

	return uuid.Nil, nil, "", false
}

// GetConfirmedPassengersForRide retorna os passageiros confirmados por trecho da carona,
// combinando reservas locais e bookings de passageiros vindos de outras empresas do consórcio.
func (r *Repository) GetConfirmedPassengersForRide(rideID uuid.UUID) (map[uuid.UUID][]models.SegmentBooking, error) {
	r.ridesMu.RLock()
	defer r.ridesMu.RUnlock()

	r.reservationsMu.RLock()
	defer r.reservationsMu.RUnlock()

	r.bookingsMu.RLock()
	defer r.bookingsMu.RUnlock()

	r.usersMu.RLock()
	defer r.usersMu.RUnlock()

	if _, exists := r.rides[rideID]; !exists {
		return nil, errors.New("carona não encontrada")
	}

	bySegment := make(map[uuid.UUID][]models.SegmentBooking)
	seen := make(map[uuid.UUID]map[uuid.UUID]struct{})

	addPassenger := func(segID uuid.UUID, entry models.SegmentBooking) {
		if seen[segID] == nil {
			seen[segID] = make(map[uuid.UUID]struct{})
		}
		if _, exists := seen[segID][entry.PassengerID]; exists {
			return
		}
		seen[segID][entry.PassengerID] = struct{}{}
		bySegment[segID] = append(bySegment[segID], entry)
	}

	// 1. Passageiros locais (via r.reservations)
	for _, reservation := range r.reservations {
		if reservation.Status != enum.Confirmada {
			continue
		}
		for _, seg := range reservation.Segments {
			if seg.RideID != rideID {
				continue
			}
			name := ""
			email := ""
			if u, ok := r.userByIDLocked(reservation.PassengerID); ok {
				name = u.Name
				email = u.Email
			}
			addPassenger(seg.SegmentID, models.SegmentBooking{
				ReservationID:  reservation.ID,
				RideID:         rideID,
				SegmentID:      seg.SegmentID,
				PassengerID:    reservation.PassengerID,
				PassengerName:  name,
				PassengerEmail: email,
				CompanyID:      seg.CompanyID,
				CreatedAt:      reservation.CreatedAt,
			})
		}
	}

	// 2. Passageiros remotos (via r.bookings)
	for _, b := range r.bookings {
		if b.Cancelled || b.RideID != rideID {
			continue
		}
		addPassenger(b.SegmentID, *cloneSegmentBooking(b))
	}

	for segID := range bySegment {
		sort.Slice(bySegment[segID], func(i, j int) bool {
			return bySegment[segID][i].PassengerID.String() < bySegment[segID][j].PassengerID.String()
		})
	}

	return bySegment, nil
}
