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

func (r *Repository) loadReservations() error {
	var reservations []models.Reservation
	if err := readJSONFile(r.reservationsPath, &reservations); err != nil {
		return err
	}

	for _, reservation := range reservations {
		if !reservation.IsValid() {
			return errors.New("arquivo de reservas contém um registro inválido")
		}
		if _, exists := r.reservations[reservation.ID]; exists {
			return errors.New("arquivo de reservas contém identificadores duplicados")
		}

		reservationCopy := reservation
		r.reservations[reservation.ID] = &reservationCopy
	}

	return nil
}

func (r *Repository) ConfirmReservation(passengerID uuid.UUID, segments []models.ReservedSegment) (*models.Reservation, error) {
	if passengerID == uuid.Nil || len(segments) == 0 {
		return nil, errors.New("reserva inválida")
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	r.reservationsMu.Lock()
	defer r.reservationsMu.Unlock()

	r.usersMu.RLock()
	defer r.usersMu.RUnlock()

	if _, exists := r.userByIDLocked(passengerID); !exists {
		return nil, errors.New("usuário inválido")
	}

	stages, err := r.reservedStagesLocked(segments)
	if err != nil {
		return nil, err
	}
	if !reservedStagesFormItinerary(stages) {
		return nil, errors.New("trechos não formam um itinerário válido")
	}

	for _, stage := range stages {
		if stage.stage.AvailableSeats <= 0 {
			return nil, errors.New("não há assentos disponíveis em todos os trechos")
		}
	}

	for _, stage := range stages {
		stage.stage.AvailableSeats--
	}

	reservation := &models.Reservation{
		ID:          uuid.New(),
		PassengerID: passengerID,
		Status:      enum.Confirmada,
		Segments:    append([]models.ReservedSegment(nil), segments...),
		CreatedAt:   time.Now(),
	}
	r.reservations[reservation.ID] = reservation

	if err := r.saveStateLocked(); err != nil {
		r.restoreSeats(stages)
		delete(r.reservations, reservation.ID)
		return nil, fmt.Errorf("salvar reserva e assentos: %w", err)
	}

	return cloneReservation(reservation), nil
}

// RegisterConfirmedReservation grava a reserva com status Confirmada no histórico do passageiro
// após a conclusão com sucesso do protocolo 2PC (sem decrementar assentos novamente).
func (r *Repository) RegisterConfirmedReservation(passengerID uuid.UUID, segments []models.ReservedSegment) (*models.Reservation, error) {
	return r.RegisterConfirmedReservationWithID(uuid.New(), passengerID, segments)
}

// RegisterConfirmedReservationWithID grava a reserva confirmada usando o ID informado.
func (r *Repository) RegisterConfirmedReservationWithID(reservationID, passengerID uuid.UUID, segments []models.ReservedSegment) (*models.Reservation, error) {
	if passengerID == uuid.Nil || len(segments) == 0 {
		return nil, errors.New("reserva inválida")
	}
	if reservationID == uuid.Nil {
		reservationID = uuid.New()
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.reservationsMu.Lock()
	defer r.reservationsMu.Unlock()

	r.usersMu.RLock()
	defer r.usersMu.RUnlock()

	if _, exists := r.userByIDLocked(passengerID); !exists {
		return nil, errors.New("usuário inválido")
	}

	reservation := &models.Reservation{
		ID:          reservationID,
		PassengerID: passengerID,
		Status:      enum.Confirmada,
		Segments:    append([]models.ReservedSegment(nil), segments...),
		CreatedAt:   time.Now(),
	}
	r.reservations[reservation.ID] = reservation

	if err := r.saveStateLocked(); err != nil {
		delete(r.reservations, reservation.ID)
		return nil, fmt.Errorf("salvar reserva confirmada: %w", err)
	}

	return cloneReservation(reservation), nil
}

// CountConfirmedReservationsForSegment retorna o número de reservas confirmadas locais que contêm o segmento informado.
func (r *Repository) CountConfirmedReservationsForSegment(segmentID uuid.UUID) int {
	r.reservationsMu.RLock()
	defer r.reservationsMu.RUnlock()

	count := 0
	for _, res := range r.reservations {
		if res.Status != enum.Confirmada {
			continue
		}
		for _, seg := range res.Segments {
			if seg.SegmentID == segmentID {
				count++
				break
			}
		}
	}
	return count
}

func (r *Repository) GetReservationsByPassengerID(passengerID uuid.UUID) ([]*models.Reservation, error) {
	r.reservationsMu.RLock()
	defer r.reservationsMu.RUnlock()

	reservations := make([]*models.Reservation, 0)
	for _, reservation := range r.reservations {
		if reservation.PassengerID == passengerID {
			reservations = append(reservations, cloneReservation(reservation))
		}
	}

	sort.Slice(reservations, func(i, j int) bool {
		return reservations[i].CreatedAt.After(reservations[j].CreatedAt)
	})
	return reservations, nil
}

// GetReservationByID busca uma reserva pelo seu identificador único.
func (r *Repository) GetReservationByID(id uuid.UUID) (*models.Reservation, error) {
	r.reservationsMu.RLock()
	defer r.reservationsMu.RUnlock()

	reservation, exists := r.reservations[id]
	if !exists {
		return nil, errors.New("reserva não encontrada")
	}

	return cloneReservation(reservation), nil
}

// GetConfirmedPassengerIDsByRideID returns each passenger only once per
// segment. Canceled reservations are deliberately excluded.
func (r *Repository) GetConfirmedPassengerIDsByRideID(rideID uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	r.ridesMu.RLock()
	defer r.ridesMu.RUnlock()

	r.reservationsMu.RLock()
	defer r.reservationsMu.RUnlock()

	if _, exists := r.rides[rideID]; !exists {
		return nil, errors.New("carona não encontrada")
	}

	passengersBySegment := make(map[uuid.UUID][]uuid.UUID)
	seenPassengers := make(map[uuid.UUID]map[uuid.UUID]struct{})
	for _, reservation := range r.reservations {
		if reservation.Status != enum.Confirmada {
			continue
		}

		for _, reservedSegment := range reservation.Segments {
			if reservedSegment.RideID != rideID {
				continue
			}

			if seenPassengers[reservedSegment.SegmentID] == nil {
				seenPassengers[reservedSegment.SegmentID] = make(map[uuid.UUID]struct{})
			}
			if _, exists := seenPassengers[reservedSegment.SegmentID][reservation.PassengerID]; exists {
				continue
			}

			seenPassengers[reservedSegment.SegmentID][reservation.PassengerID] = struct{}{}
			passengersBySegment[reservedSegment.SegmentID] = append(passengersBySegment[reservedSegment.SegmentID], reservation.PassengerID)
		}
	}

	for segmentID := range passengersBySegment {
		sort.Slice(passengersBySegment[segmentID], func(i, j int) bool {
			return passengersBySegment[segmentID][i].String() < passengersBySegment[segmentID][j].String()
		})
	}

	return passengersBySegment, nil
}

func (r *Repository) CancelReservation(reservationID, passengerID uuid.UUID, companyID string) ([]models.ReservedSegment, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	r.reservationsMu.Lock()
	defer r.reservationsMu.Unlock()

	reservation, exists := r.reservations[reservationID]
	if !exists {
		return nil, errors.New("reserva não encontrada")
	}
	if reservation.PassengerID != passengerID {
		return nil, errors.New("passageiro não pode cancelar esta reserva")
	}
	if reservation.Status != enum.Confirmada {
		return nil, errors.New("reserva não pode ser cancelada")
	}

	var localStages []reservedStage
	for _, segment := range reservation.Segments {
		if companyID == "" || segment.CompanyID == "" || segment.CompanyID == companyID {
			ride, exists := r.rides[segment.RideID]
			if exists && !ride.Cancelled {
				for index := range ride.Segments {
					if ride.Segments[index].ID == segment.SegmentID {
						localStages = append(localStages, reservedStage{stage: &ride.Segments[index]})
						break
					}
				}
			}
		}
	}

	for _, stage := range localStages {
		stage.stage.AvailableSeats++
	}

	reservation.Status = enum.Cancelada
	if err := r.saveStateLocked(); err != nil {
		r.restoreSeats(localStages)
		reservation.Status = enum.Confirmada
		return nil, fmt.Errorf("salvar cancelamento e assentos: %w", err)
	}

	return append([]models.ReservedSegment(nil), reservation.Segments...), nil
}

// CancelReservationByRideCancellation cancela de forma idempotente uma reserva
// cujo motorista de um dos trechos cancelou a carona, restaurando os assentos dos
// trechos locais ativos vinculados à mesma reserva.
func (r *Repository) CancelReservationByRideCancellation(reservationID uuid.UUID, companyID string) ([]models.ReservedSegment, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	r.reservationsMu.Lock()
	defer r.reservationsMu.Unlock()

	reservation, exists := r.reservations[reservationID]
	if !exists {
		return nil, nil
	}
	if reservation.Status != enum.Confirmada {
		return nil, nil
	}

	var localStages []reservedStage
	for _, segment := range reservation.Segments {
		if companyID == "" || segment.CompanyID == "" || segment.CompanyID == companyID {
			ride, exists := r.rides[segment.RideID]
			if exists && !ride.Cancelled {
				for index := range ride.Segments {
					if ride.Segments[index].ID == segment.SegmentID {
						localStages = append(localStages, reservedStage{stage: &ride.Segments[index]})
						break
					}
				}
			}
		}
	}

	for _, stage := range localStages {
		stage.stage.AvailableSeats++
	}

	reservation.Status = enum.Cancelada
	if err := r.saveStateLocked(); err != nil {
		r.restoreSeats(localStages)
		reservation.Status = enum.Confirmada
		return nil, fmt.Errorf("salvar cancelamento por carona cancelada: %w", err)
	}

	return append([]models.ReservedSegment(nil), reservation.Segments...), nil
}

type reservedStage struct {
	stage *models.Stage
}

type affectedReservation struct {
	reservation *models.Reservation
	stages      []reservedStage
}

// confirmedReservationsForRideLocked gathers every valid confirmed reservation
// that uses the ride. It validates all local data before changing any seat or status,
// and collects remote segments that must be released when a multi-company reservation
// includes segments operated by other consortium companies.
func (r *Repository) confirmedReservationsForRideLocked(rideID uuid.UUID, companyID string) ([]affectedReservation, []RemoteSegmentRelease, error) {
	affected := make([]affectedReservation, 0)
	remoteReleases := make([]RemoteSegmentRelease, 0)

	for _, reservation := range r.reservations {
		if reservation.Status != enum.Confirmada || !reservationContainsRide(reservation, rideID) {
			continue
		}

		stages := make([]reservedStage, 0, len(reservation.Segments))
		seen := make(map[models.ReservedSegment]struct{}, len(reservation.Segments))
		for _, segment := range reservation.Segments {
			if !segment.IsValid() {
				return nil, nil, fmt.Errorf("reserva %s inválida: trecho reservado inválido", reservation.ID)
			}
			if _, exists := seen[segment]; exists {
				return nil, nil, fmt.Errorf("reserva %s inválida: trecho reservado repetido", reservation.ID)
			}
			seen[segment] = struct{}{}

			isRemote := (companyID != "" && segment.CompanyID != "" && segment.CompanyID != companyID) ||
				(companyID == "" && segment.CompanyID != "" && r.rides[segment.RideID] == nil)
			if isRemote {
				remoteReleases = append(remoteReleases, RemoteSegmentRelease{
					CompanyID:     segment.CompanyID,
					RideID:        segment.RideID,
					SegmentID:     segment.SegmentID,
					ReservationID: reservation.ID,
					PassengerID:   reservation.PassengerID,
				})
				continue
			}

			localRide, exists := r.rides[segment.RideID]
			if !exists {
				return nil, nil, fmt.Errorf("reserva %s inválida: carona indisponível", reservation.ID)
			}

			found := false
			for index := range localRide.Segments {
				if localRide.Segments[index].ID == segment.SegmentID {
					if !localRide.Cancelled {
						stages = append(stages, reservedStage{stage: &localRide.Segments[index]})
					}
					found = true
					break
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("reserva %s inválida: trecho não encontrado", reservation.ID)
			}
		}

		affected = append(affected, affectedReservation{reservation: reservation, stages: stages})
	}

	return affected, remoteReleases, nil
}

func reservationContainsRide(reservation *models.Reservation, rideID uuid.UUID) bool {
	for _, segment := range reservation.Segments {
		if segment.RideID == rideID {
			return true
		}
	}
	return false
}

func (r *Repository) restoreRideCancellation(ride *models.Ride, affected []affectedReservation) {
	ride.Cancelled = false
	for _, item := range affected {
		item.reservation.Status = enum.Confirmada
		for _, stage := range item.stages {
			stage.stage.AvailableSeats--
		}
	}
}

func (r *Repository) reservedStagesLocked(segments []models.ReservedSegment) ([]reservedStage, error) {
	stages := make([]reservedStage, 0, len(segments))
	seen := make(map[models.ReservedSegment]struct{}, len(segments))
	for _, segment := range segments {
		if !segment.IsValid() {
			return nil, errors.New("trecho reservado inválido")
		}
		if _, exists := seen[segment]; exists {
			return nil, errors.New("trecho reservado repetido")
		}
		seen[segment] = struct{}{}

		ride, exists := r.rides[segment.RideID]
		if !exists || !ride.CanBook() {
			return nil, errors.New("carona indisponível")
		}

		found := false
		for index := range ride.Segments {
			if ride.Segments[index].ID == segment.SegmentID {
				if ride.Segments[index].GetStatus() == models.StageStatusCompleted {
					return nil, errors.New("trecho já concluído")
				}
				stages = append(stages, reservedStage{stage: &ride.Segments[index]})
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("trecho não encontrado")
		}
	}

	return stages, nil
}

func reservedStagesFormItinerary(stages []reservedStage) bool {
	for index, stage := range stages {
		if !stage.stage.IsValid() {
			return false
		}
		if index == 0 {
			continue
		}

		previous := stages[index-1]
		if previous.stage.Destination != stage.stage.Origin {
			return false
		}

		if stage.stage.DepartureAt.Before(previous.stage.ArrivalAt) {
			return false
		}
	}

	return len(stages) > 0
}

func (r *Repository) restoreSeats(stages []reservedStage) {
	for _, stage := range stages {
		stage.stage.AvailableSeats++
	}
}
