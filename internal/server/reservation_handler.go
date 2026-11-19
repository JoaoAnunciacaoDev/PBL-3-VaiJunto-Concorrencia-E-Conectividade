package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/consortium"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/enum"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/protocol"
	"github.com/google/uuid"
)

func (s *Server) handleConfirmReservation(payload json.RawMessage, session *Session) protocol.Response {
	if response, ok := s.passengerForSession(session); !ok {
		return response
	}

	var dto protocol.ConfirmReservationRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil {
		return protocol.Response{Success: "error", Message: "Requisição inválida: " + err.Error()}
	}

	if len(dto.Segments) == 0 {
		return protocol.Response{Success: "error", Message: "Nenhum trecho informado para reserva."}
	}

	// Normaliza o CompanyID para segmentos que porventura não vieram preenchidos
	for i := range dto.Segments {
		if dto.Segments[i].CompanyID == "" {
			dto.Segments[i].CompanyID = s.companyID
		}
	}

	// Validação de reserva duplicada: impede que o mesmo passageiro reserve novamente
	// um itinerário contendo trechos para os quais já possui reserva confirmada ativa.
	existingReservations, err := s.repository.GetReservationsByPassengerID(session.UserID)
	if err == nil {
		for _, res := range existingReservations {
			if res.Status != enum.Confirmada {
				continue
			}
			for _, existingSeg := range res.Segments {
				for _, reqSeg := range dto.Segments {
					if existingSeg.SegmentID == reqSeg.SegmentID {
						return protocol.Response{
							Success: "error",
							Message: "Você já possui uma reserva confirmada para este itinerário.",
						}
					}
				}
			}
		}
	}

	// Se o coordenador 2PC estiver habilitado, executa o Two-Phase Commit distribuído
	if s.coordinator != nil {
		reservationID := uuid.New()
		passengerName := session.UserName
		passengerEmail := ""
		if u, uErr := s.repository.GetUserByID(session.UserID); uErr == nil {
			if u.Name != "" {
				passengerName = u.Name
			}
			passengerEmail = u.Email
		}

		bookingInfo := consortium.PassengerBookingInfo{
			ReservationID:  reservationID,
			PassengerID:    session.UserID,
			PassengerName:  passengerName,
			PassengerEmail: passengerEmail,
		}

		if err := s.coordinator.ConfirmItineraryWithPassenger(bookingInfo, dto.Segments); err != nil {
			return protocol.Response{Success: "error", Message: err.Error()}
		}

		reservation, err := s.repository.RegisterConfirmedReservationWithID(reservationID, session.UserID, dto.Segments)
		if err != nil {
			return protocol.Response{Success: "error", Message: "Erro ao registrar reserva: " + err.Error()}
		}

		return reservationResponse(reservation, "Reserva confirmada com sucesso.")
	}

	// Fallback para modo local puro
	reservation, err := s.repository.ConfirmReservation(session.UserID, dto.Segments)
	if err != nil {
		return protocol.Response{Success: "error", Message: err.Error()}
	}

	return reservationResponse(reservation, "Reserva confirmada com sucesso.")
}

func (s *Server) handleListMyReservations(session *Session) protocol.Response {
	if response, ok := s.passengerForSession(session); !ok {
		return response
	}

	reservations, err := s.repository.GetReservationsByPassengerID(session.UserID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Não foi possível consultar as reservas."}
	}

	responses := make([]protocol.ReservationResponse, 0, len(reservations))
	for _, reservation := range reservations {
		response, err := s.reservationResponse(*reservation)
		if err != nil {
			return protocol.Response{Success: "error", Message: "Não foi possível consultar os trechos das reservas."}
		}
		responses = append(responses, response)
	}

	return reservationResponse(responses, "Reservas obtidas com sucesso.")
}

func (s *Server) reservationResponse(reservation models.Reservation) (protocol.ReservationResponse, error) {
	segments := make([]protocol.ReservationStageResponse, 0, len(reservation.Segments))
	for _, reservedSegment := range reservation.Segments {
		var stage *models.Stage
		var compID, driverName string
		var driverID uuid.UUID
		compID = reservedSegment.CompanyID
		if compID == "" {
			compID = s.companyID
		}

		// Se a carona é local, busca do repositório local
		ride, err := s.repository.GetRideByID(reservedSegment.RideID)
		if err == nil {
			driverID = ride.DriverID
			driverName = ride.DriverName
			if driverName == "" {
				if d, dErr := s.repository.GetUserByID(ride.DriverID); dErr == nil {
					driverName = d.Name
				}
			}
			for index := range ride.Segments {
				if ride.Segments[index].ID == reservedSegment.SegmentID {
					stage = &ride.Segments[index]
					break
				}
			}
		}

		// Se não encontrou localmente e pertence a um parceiro, busca do peer
		if stage == nil && reservedSegment.CompanyID != "" &&
			reservedSegment.CompanyID != s.companyID {

			if s.peers == nil {
				return protocol.ReservationResponse{}, fmt.Errorf("registro de parceiros indisponível")
			}

			peer, exists := s.peers.Get(reservedSegment.CompanyID)
			if !exists {
				return protocol.ReservationResponse{}, fmt.Errorf(
					"empresa %s não encontrada", reservedSegment.CompanyID,
				)
			}

			remoteRide, err := s.fetchRemoteRide(peer.InternalURL, reservedSegment.RideID)
			if err != nil {
				return protocol.ReservationResponse{}, fmt.Errorf(
					"buscar carona %s na empresa %s: %w",
					reservedSegment.RideID, reservedSegment.CompanyID, err,
				)
			}

			driverID = remoteRide.DriverID
			driverName = remoteRide.DriverName

			for index := range remoteRide.Segments {
				if remoteRide.Segments[index].ID == reservedSegment.SegmentID {
					stage = &remoteRide.Segments[index]
					break
				}
			}
		}

		if stage == nil {
			return protocol.ReservationResponse{}, fmt.Errorf(
				"trecho %s não encontrado na carona %s da empresa %s",
				reservedSegment.SegmentID,
				reservedSegment.RideID,
				compID,
			)
		}

		segments = append(segments, protocol.ReservationStageResponse{
			RideID:      reservedSegment.RideID,
			SegmentID:   reservedSegment.SegmentID,
			CompanyID:   compID,
			DriverID:    driverID,
			DriverName:  driverName,
			Status:      stage.GetStatus(),
			Origin:      stage.Origin,
			Destination: stage.Destination,
			DepartureAt: stage.DepartureAt,
			ArrivalAt:   stage.ArrivalAt,
			PriceCents:  stage.PriceCents,
		})
	}

	return protocol.ReservationResponse{
		ID:        reservation.ID,
		Status:    reservation.Status,
		CreatedAt: reservation.CreatedAt,
		Segments:  segments,
	}, nil
}

func (s *Server) handleCancelReservation(payload json.RawMessage, session *Session) protocol.Response {
	if response, ok := s.passengerForSession(session); !ok {
		return response
	}

	var dto protocol.CancelReservationRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil || dto.ReservationID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificador da reserva inválido."}
	}

	reservation, err := s.repository.GetReservationByID(dto.ReservationID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Reserva não encontrada."}
	}
	if reservation.PassengerID != session.UserID {
		return protocol.Response{Success: "error", Message: "Passageiro não pode cancelar esta reserva."}
	}
	if reservation.Status != enum.Confirmada {
		return protocol.Response{Success: "error", Message: "Reserva não pode ser cancelada."}
	}

	var earliestDeparture time.Time
	for _, seg := range reservation.Segments {
		compID := seg.CompanyID
		if compID == "" {
			compID = s.companyID
		}
		ride, rErr := s.getRideLocalOrRemote(compID, seg.RideID)
		if rErr != nil {
			continue
		}
		rideStatus := ride.GetStatus()
		if rideStatus == models.RideStatusInProgress || rideStatus == models.RideStatusCompleted || ride.IsSegmentCompleted(seg.SegmentID) {
			return protocol.Response{
				Success: "error",
				Message: "Não é possível cancelar uma reserva cuja viagem ou trecho já foi iniciada ou concluída.",
			}
		}
		for _, stage := range ride.Segments {
			if stage.ID == seg.SegmentID {
				if earliestDeparture.IsZero() || stage.DepartureAt.Before(earliestDeparture) {
					earliestDeparture = stage.DepartureAt
				}
				break
			}
		}
	}

	segments, err := s.repository.CancelReservation(dto.ReservationID, session.UserID, s.companyID)
	if err != nil {
		return protocol.Response{Success: "error", Message: err.Error()}
	}

	for _, seg := range segments {
		if seg.CompanyID != "" && seg.CompanyID != s.companyID {
			s.releaseRemoteSegmentWithRetry(seg.CompanyID, seg.RideID, seg.SegmentID, dto.ReservationID, session.UserID)
		}
	}

	if !earliestDeparture.IsZero() && time.Until(earliestDeparture) < 2*time.Hour {
		lateRecord := &models.Rating{
			ID:            uuid.New(),
			RideID:        segments[0].RideID,
			ReservationID: dto.ReservationID,
			AuthorID:      session.UserID,
			AuthorName:    session.UserName,
			TargetID:      session.UserID,
			TargetName:    session.UserName,
			Type:          models.RatingTypeLateCancellation,
			Score:         0,
			Comment:       "Cancelamento tardio efetuado com menos de 2 horas de antecedência da partida.",
			CreatedAt:     time.Now(),
		}
		if err := s.repository.SaveRating(lateRecord); err != nil {
			log.Printf("[cancel] falha ao registrar cancelamento tardio: %v", err)
		} else {
			s.replicateRatingToPeers(lateRecord)
		}
	}

	return protocol.Response{Success: "success", Message: "Reserva cancelada com sucesso."}
}

func (s *Server) handleIncomingRemoteRideCancelled(reservationID uuid.UUID, cancelledCompanyID string) error {
	segments, err := s.repository.CancelReservationByRideCancellation(reservationID, s.companyID)
	if err != nil {
		return err
	}

	for _, seg := range segments {
		if seg.CompanyID != "" && seg.CompanyID != s.companyID && seg.CompanyID != cancelledCompanyID {
			s.releaseRemoteSegmentWithRetry(seg.CompanyID, seg.RideID, seg.SegmentID, reservationID, uuid.Nil)
		}
	}

	return nil
}

func (s *Server) releaseRemoteSegmentWithRetry(companyID string, rideID, segmentID, reservationID, passengerID uuid.UUID) {
	if s.peers == nil {
		return
	}
	peer, exists := s.peers.Get(companyID)
	if !exists {
		log.Printf("[cancel] peer %s não encontrado para liberar assento do trecho %s", companyID, segmentID)
		return
	}

	payloadMap := map[string]any{
		"ride_id":                rideID,
		"coordinator_company_id": s.companyID,
	}
	if reservationID != uuid.Nil {
		payloadMap["reservation_id"] = reservationID
	}
	if passengerID != uuid.Nil {
		payloadMap["passenger_id"] = passengerID
	}
	body, _ := json.Marshal(payloadMap)

	url := fmt.Sprintf("%s/internal/v1/segments/%s/release", peer.InternalURL, segmentID)
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			log.Printf("[cancel] erro ao criar requisição de release para %s: %v", url, err)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				log.Printf("[cancel] assento do trecho %s liberado com sucesso na empresa %s", segmentID, companyID)
				return
			}
			log.Printf("[cancel] tentativa %d release trecho %s na empresa %s retornou HTTP %d", attempt, segmentID, companyID, resp.StatusCode)
		} else {
			log.Printf("[cancel] tentativa %d falha ao notificar release trecho %s para empresa %s: %v", attempt, segmentID, companyID, err)
		}

		if attempt < 3 {
			time.Sleep(150 * time.Millisecond)
		}
	}
}

func reservationResponse(data any, message string) protocol.Response {
	payload, err := json.Marshal(data)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Não foi possível processar a resposta."}
	}

	return protocol.Response{Success: "success", Message: message, Payload: payload}
}

func (s *Server) fetchRemoteRide(baseURL string, rideID uuid.UUID) (*models.Ride, error) {
	url := fmt.Sprintf("%s/internal/v1/rides/%s", baseURL, rideID)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peer retornou status %d para carona %s", resp.StatusCode, rideID)
	}

	var ride models.Ride
	if err := json.NewDecoder(resp.Body).Decode(&ride); err != nil {
		return nil, err
	}

	return &ride, nil
}
