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

func (r *Repository) loadRides() error {
	var rides []models.Ride
	if err := readJSONFile(r.ridesPath, &rides); err != nil {
		return err
	}

	for _, ride := range rides {
		if !ride.IsValid() {
			return errors.New("arquivo de caronas contém um registro inválido")
		}
		if _, exists := r.rides[ride.ID]; exists {
			return errors.New("arquivo de caronas contém identificadores duplicados")
		}

		rideCopy := ride
		r.rides[ride.ID] = &rideCopy
	}

	return nil
}

func (r *Repository) SaveRide(ride *models.Ride) error {
	if ride == nil || !ride.IsValid() {
		return errors.New("carona inválida")
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	oldRide, existed := r.rides[ride.ID]
	r.rides[ride.ID] = cloneRide(ride)
	if err := r.saveStateLocked(); err != nil {
		if existed {
			r.rides[ride.ID] = oldRide
		} else {
			delete(r.rides, ride.ID)
		}
		return fmt.Errorf("salvar carona: %w", err)
	}

	return nil
}

// SaveRideForDriver performs the final vehicle-capacity check while holding the
// same lock used by vehicle updates and removals.
func (r *Repository) SaveRideForDriver(ride *models.Ride) error {
	if ride == nil || !ride.IsValid() {
		return errors.New("carona inválida")
	}

	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.driversMu.RLock()
	defer r.driversMu.RUnlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	driver, exists := r.drivers[ride.DriverID]
	if !exists || driver.Vehicle == nil {
		return errors.New("nenhum veículo cadastrado")
	}
	if ride.DriverName == "" && driver.Name != "" {
		ride.DriverName = driver.Name
	}
	for _, stage := range ride.Segments {
		if stage.AvailableSeats > driver.Vehicle.SeatCapacity {
			return errors.New("a capacidade da carona excede a capacidade atual do veículo")
		}
	}

	oldRide, existed := r.rides[ride.ID]
	r.rides[ride.ID] = cloneRide(ride)
	if err := r.saveStateLocked(); err != nil {
		if existed {
			r.rides[ride.ID] = oldRide
		} else {
			delete(r.rides, ride.ID)
		}
		return fmt.Errorf("salvar carona: %w", err)
	}

	return nil
}

func (r *Repository) GetRideByID(id uuid.UUID) (*models.Ride, error) {
	r.ridesMu.RLock()
	defer r.ridesMu.RUnlock()

	ride, exists := r.rides[id]
	if !exists {
		return nil, errors.New("carona não encontrada")
	}

	c := cloneRide(ride)
	if c.DriverName == "" {
		r.usersMu.RLock()
		if u, ok := r.userByIDLocked(c.DriverID); ok {
			c.DriverName = u.Name
		}
		r.usersMu.RUnlock()
	}

	return c, nil
}

// GetRideBySegmentID busca a carona que contém o segmento especificado.
func (r *Repository) GetRideBySegmentID(segmentID uuid.UUID) (*models.Ride, error) {
	r.ridesMu.RLock()
	defer r.ridesMu.RUnlock()

	for _, ride := range r.rides {
		for _, s := range ride.Segments {
			if s.ID == segmentID {
				c := cloneRide(ride)
				if c.DriverName == "" {
					r.usersMu.RLock()
					if u, ok := r.userByIDLocked(c.DriverID); ok {
						c.DriverName = u.Name
					}
					r.usersMu.RUnlock()
				}
				return c, nil
			}
		}
	}

	return nil, errors.New("carona contendo o trecho não encontrada")
}

func (r *Repository) GetRidesByDriverID(driverID uuid.UUID) ([]*models.Ride, error) {
	r.ridesMu.RLock()
	defer r.ridesMu.RUnlock()

	rides := make([]*models.Ride, 0)
	for _, ride := range r.rides {
		if ride.DriverID == driverID {
			rides = append(rides, cloneRide(ride))
		}
	}

	sort.Slice(rides, func(i, j int) bool {
		return rides[i].DepartureAt.Before(rides[j].DepartureAt)
	})

	return rides, nil
}

func (r *Repository) GetActiveRidesByDate(date time.Time) ([]*models.Ride, error) {
	r.ridesMu.RLock()
	defer r.ridesMu.RUnlock()

	r.usersMu.RLock()
	defer r.usersMu.RUnlock()

	rides := make([]*models.Ride, 0)
	for _, ride := range r.rides {
		if ride.CanBook() && sameCalendarDate(ride.DepartureAt, date) {
			c := cloneRide(ride)
			if c.DriverName == "" {
				if u, ok := r.userByIDLocked(c.DriverID); ok {
					c.DriverName = u.Name
				}
			}
			rides = append(rides, c)
		}
	}

	sort.Slice(rides, func(i, j int) bool {
		return rides[i].DepartureAt.Before(rides[j].DepartureAt)
	})

	return rides, nil
}

// RemoteSegmentRelease identifica um trecho remoto vinculado a uma reserva local
// que precisa ter seu assento liberado quando uma carona local da reserva é cancelada.
type RemoteSegmentRelease struct {
	CompanyID     string
	RideID        uuid.UUID
	SegmentID     uuid.UUID
	ReservationID uuid.UUID
	PassengerID   uuid.UUID
}

// CancelRideResult resume os efeitos colaterais de rede necessários após o cancelamento local de uma carona.
type CancelRideResult struct {
	HadPassengers           bool
	RemoteBookings          []models.SegmentBooking
	RemoteSegmentsToRelease []RemoteSegmentRelease
}

func (r *Repository) CancelRide(rideID, driverID uuid.UUID) error {
	_, err := r.CancelRideDetailed(rideID, driverID, "")
	return err
}

// CancelRideDetailed cancela uma carona agendada do motorista, cancelando todas as
// reservas locais vinculadas, marcando os bookings remotos como cancelados e restaurando
// os assentos locais. Retorna se havia passageiros confirmados, bookings remotos afetados
// e eventuais trechos remotos de reservas locais multi-empresa que precisam ser liberados.
func (r *Repository) CancelRideDetailed(rideID, driverID uuid.UUID, companyID string) (CancelRideResult, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	r.reservationsMu.Lock()
	defer r.reservationsMu.Unlock()

	r.bookingsMu.Lock()
	defer r.bookingsMu.Unlock()

	ride, exists := r.rides[rideID]
	if !exists {
		return CancelRideResult{}, errors.New("carona não encontrada")
	}
	if ride.DriverID != driverID {
		return CancelRideResult{}, errors.New("motorista não pode cancelar esta carona")
	}
	if ride.Cancelled || ride.GetStatus() == models.RideStatusCancelled {
		return CancelRideResult{}, errors.New("carona já está cancelada")
	}
	if !ride.CanCancel() {
		return CancelRideResult{}, errors.New("não é possível cancelar uma carona que já foi iniciada ou concluída")
	}

	affectedReservations, remoteReleases, err := r.confirmedReservationsForRideLocked(rideID, companyID)
	if err != nil {
		return CancelRideResult{}, fmt.Errorf("cancelar reservas da carona: %w", err)
	}

	prevStatus := ride.Status
	ride.Cancel()

	restoredLocal := make(map[string]bool)
	for _, affected := range affectedReservations {
		affected.reservation.Status = enum.Cancelada
		for _, stage := range affected.stages {
			stage.stage.AvailableSeats++
			restoredLocal[affected.reservation.ID.String()+":"+stage.stage.ID.String()] = true
		}
	}

	type cancelledBookingItem struct {
		booking   *models.SegmentBooking
		stageIdx  int
		seatAdded bool
	}
	var cancelledItems []cancelledBookingItem
	var remoteBookings []models.SegmentBooking

	for _, b := range r.bookings {
		if b.Cancelled || b.RideID != rideID {
			continue
		}
		b.Cancelled = true
		item := cancelledBookingItem{booking: b, stageIdx: -1}
		key := b.ReservationID.String() + ":" + b.SegmentID.String()
		if !restoredLocal[key] {
			for idx := range ride.Segments {
				if ride.Segments[idx].ID == b.SegmentID {
					ride.Segments[idx].AvailableSeats++
					item.stageIdx = idx
					item.seatAdded = true
					break
				}
			}
		}
		cancelledItems = append(cancelledItems, item)
		if b.ReservationID != uuid.Nil {
			remoteBookings = append(remoteBookings, *cloneSegmentBooking(b))
		}
	}

	if err := r.saveStateLocked(); err != nil {
		r.restoreRideCancellation(ride, affectedReservations)
		ride.Status = prevStatus
		for _, item := range cancelledItems {
			item.booking.Cancelled = false
			if item.seatAdded && item.stageIdx >= 0 && item.stageIdx < len(ride.Segments) {
				ride.Segments[item.stageIdx].AvailableSeats--
			}
		}
		return CancelRideResult{}, fmt.Errorf("cancelar carona: %w", err)
	}

	hadPassengers := len(affectedReservations) > 0 || len(cancelledItems) > 0 || len(remoteReleases) > 0
	return CancelRideResult{
		HadPassengers:           hadPassengers,
		RemoteBookings:          remoteBookings,
		RemoteSegmentsToRelease: remoteReleases,
	}, nil
}

func sameCalendarDate(first, second time.Time) bool {
	firstYear, firstMonth, firstDay := first.Date()
	secondYear, secondMonth, secondDay := second.Date()
	return firstYear == secondYear && firstMonth == secondMonth && firstDay == secondDay
}

// DecrementSeatIdempotent decrementa em 1 os assentos disponíveis de um trecho específico
// garantindo que o mesmo tx_id não provoque dois decrementos, mesmo após falhas ou retransmissões.
func (r *Repository) DecrementSeatIdempotent(rideID, segmentID uuid.UUID, txID string) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	key := ""
	if txID != "" {
		key = txID + ":" + segmentID.String()
		if r.appliedTx[key] {
			// Já aplicado anteriormente com sucesso: idempotência garantida
			return nil
		}
	}

	ride, exists := r.rides[rideID]
	if !exists || !ride.CanBook() {
		return errors.New("carona indisponível")
	}

	for i := range ride.Segments {
		if ride.Segments[i].ID == segmentID {
			if ride.Segments[i].GetStatus() == models.StageStatusCompleted {
				return errors.New("trecho já concluído")
			}
			if ride.Segments[i].AvailableSeats <= 0 {
				return errors.New("não há assentos disponíveis no trecho")
			}
			ride.Segments[i].AvailableSeats--
			if key != "" {
				r.appliedTx[key] = true
			}
			if err := r.saveStateLocked(); err != nil {
				ride.Segments[i].AvailableSeats++
				if key != "" {
					delete(r.appliedTx, key)
				}
				return fmt.Errorf("salvar estado após decrementar assento: %w", err)
			}
			return nil
		}
	}

	return errors.New("trecho não encontrado")
}

// DecrementSeat decrementa em 1 os assentos disponíveis de um trecho específico e persiste o estado.
func (r *Repository) DecrementSeat(rideID, segmentID uuid.UUID) error {
	return r.DecrementSeatIdempotent(rideID, segmentID, "")
}

// IsTxApplied verifica se a transação especificada já teve o efeito de decremento aplicado no repositório.
func (r *Repository) IsTxApplied(txID string, segmentID uuid.UUID) bool {
	r.ridesMu.RLock()
	defer r.ridesMu.RUnlock()

	if txID == "" {
		return false
	}
	key := txID + ":" + segmentID.String()
	return r.appliedTx[key]
}

// IncrementSeat incrementa em 1 os assentos disponíveis de um trecho específico e persiste o estado.
func (r *Repository) IncrementSeat(rideID, segmentID uuid.UUID) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	ride, exists := r.rides[rideID]
	if !exists || !ride.CanBook() {
		return errors.New("carona indisponível")
	}

	for i := range ride.Segments {
		if ride.Segments[i].ID == segmentID {
			if ride.Segments[i].GetStatus() == models.StageStatusCompleted {
				return errors.New("trecho já concluído")
			}
			ride.Segments[i].AvailableSeats++
			if err := r.saveStateLocked(); err != nil {
				ride.Segments[i].AvailableSeats--
				return fmt.Errorf("salvar estado após incrementar assento: %w", err)
			}
			return nil
		}
	}

	return errors.New("trecho não encontrado")
}

// StartRide marca uma carona como em andamento (IN_PROGRESS) pelo motorista responsável.
func (r *Repository) StartRide(rideID, driverID uuid.UUID) (*models.Ride, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	ride, exists := r.rides[rideID]
	if !exists {
		return nil, errors.New("carona não encontrada")
	}
	if ride.DriverID != driverID {
		return nil, errors.New("a carona não pertence ao motorista autenticado")
	}

	oldRide := cloneRide(ride)
	if err := ride.Start(time.Now()); err != nil {
		return nil, err
	}

	if err := r.saveStateLocked(); err != nil {
		r.rides[rideID] = oldRide
		return nil, fmt.Errorf("salvar estado ao iniciar carona: %w", err)
	}

	return cloneRide(ride), nil
}

// FinishSegment conclui um trecho específico de uma carona pelo motorista responsável.
func (r *Repository) FinishSegment(rideID, segmentID, driverID uuid.UUID) (*models.Ride, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	ride, exists := r.rides[rideID]
	if !exists {
		return nil, errors.New("carona não encontrada")
	}
	if ride.DriverID != driverID {
		return nil, errors.New("a carona não pertence ao motorista autenticado")
	}

	oldRide := cloneRide(ride)
	if err := ride.FinishSegment(segmentID, time.Now()); err != nil {
		return nil, err
	}

	if err := r.saveStateLocked(); err != nil {
		r.rides[rideID] = oldRide
		return nil, fmt.Errorf("salvar estado ao finalizar trecho: %w", err)
	}

	return cloneRide(ride), nil
}

// FinishRide marca uma carona como concluída (COMPLETED) pelo motorista responsável.
func (r *Repository) FinishRide(rideID, driverID uuid.UUID) (*models.Ride, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	ride, exists := r.rides[rideID]
	if !exists {
		return nil, errors.New("carona não encontrada")
	}
	if ride.DriverID != driverID {
		return nil, errors.New("a carona não pertence ao motorista autenticado")
	}

	oldRide := cloneRide(ride)
	if err := ride.Finish(time.Now()); err != nil {
		return nil, err
	}

	if err := r.saveStateLocked(); err != nil {
		r.rides[rideID] = oldRide
		return nil, fmt.Errorf("salvar estado ao finalizar carona: %w", err)
	}

	return cloneRide(ride), nil
}

// SetRideNoShow marca uma carona como finalizada com ausência de passageiro (COMPLETED_NO_SHOW).
func (r *Repository) SetRideNoShow(rideID, driverID uuid.UUID) (*models.Ride, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	r.ridesMu.Lock()
	defer r.ridesMu.Unlock()

	ride, exists := r.rides[rideID]
	if !exists {
		return nil, errors.New("carona não encontrada")
	}
	if ride.DriverID != driverID {
		return nil, errors.New("a carona não pertence ao motorista autenticado")
	}

	oldRide := cloneRide(ride)
	if err := ride.ReportNoShow(time.Now()); err != nil {
		return nil, err
	}

	if err := r.saveStateLocked(); err != nil {
		r.rides[rideID] = oldRide
		return nil, fmt.Errorf("salvar estado ao reportar no-show: %w", err)
	}

	return cloneRide(ride), nil
}
