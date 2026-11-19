package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/enum"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/protocol"
	"github.com/google/uuid"
)

func (s *Server) handleRateDriver(payload json.RawMessage, session *Session) protocol.Response {
	if response, ok := s.passengerForSession(session); !ok {
		return response
	}

	var dto protocol.RateDriverRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil {
		return protocol.Response{Success: "error", Message: "Requisição inválida: " + err.Error()}
	}

	if dto.Score < 1 || dto.Score > 5 {
		return protocol.Response{Success: "error", Message: "A nota da avaliação deve estar entre 1 e 5."}
	}
	if dto.ReservationID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificador da reserva é obrigatório para comprovar a viagem."}
	}

	reservation, err := s.repository.GetReservationByID(dto.ReservationID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Reserva não encontrada."}
	}
	if reservation.PassengerID != session.UserID {
		return protocol.Response{Success: "error", Message: "A reserva informada não pertence ao passageiro autenticado."}
	}
	if reservation.Status != enum.Confirmada {
		return protocol.Response{Success: "error", Message: "Apenas reservas confirmadas podem ser avaliadas."}
	}

	targetRide, selectedSeg, errMsg := s.resolveRideFromReservation(reservation, dto.SegmentID, dto.RideID, dto.DriverID)
	if errMsg != "" {
		return protocol.Response{Success: "error", Message: errMsg}
	}

	if s.hasPassengerNoShow(targetRide.ID, session.UserID) {
		return protocol.Response{Success: "error", Message: "Passageiros registrados como ausentes (No-show) não podem avaliar o motorista."}
	}

	if !s.isReservationPartCompleted(reservation, targetRide, selectedSeg) {
		return protocol.Response{Success: "error", Message: "O trecho da carona ainda não foi concluído pelo motorista."}
	}

	targetDriverID := targetRide.DriverID
	if s.repository.HasRatingOfType(session.UserID, targetRide.ID, targetDriverID, models.RatingTypePassengerToDriver) ||
		s.hasAggregatedRating(session.UserID, targetRide.ID, targetDriverID, models.RatingTypePassengerToDriver) {
		return protocol.Response{Success: "error", Message: "Você já avaliou este motorista para esta carona."}
	}

	driverName := targetRide.DriverName
	if driverName == "" {
		if driverUser, uErr := s.repository.GetUserByID(targetDriverID); uErr == nil {
			driverName = driverUser.Name
		}
	}

	rating := &models.Rating{
		ID:            uuid.New(),
		RideID:        targetRide.ID,
		ReservationID: reservation.ID,
		AuthorID:      session.UserID,
		AuthorName:    session.UserName,
		TargetID:      targetDriverID,
		TargetName:    driverName,
		Type:          models.RatingTypePassengerToDriver,
		Score:         dto.Score,
		Comment:       dto.Comment,
		CreatedAt:     time.Now(),
	}

	if err := s.repository.SaveRating(rating); err != nil {
		return protocol.Response{Success: "error", Message: "Falha ao registrar avaliação: " + err.Error()}
	}

	s.replicateRatingToPeers(rating)
	return reputationResponse(rating, "Avaliação do motorista registrada com sucesso.")
}

func (s *Server) handleRatePassenger(payload json.RawMessage, session *Session) protocol.Response {
	driver, response, ok := s.driverForSession(session, false)
	if !ok {
		return response
	}

	var dto protocol.RatePassengerRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil {
		return protocol.Response{Success: "error", Message: "Requisição inválida: " + err.Error()}
	}

	if dto.Score < 1 || dto.Score > 5 {
		return protocol.Response{Success: "error", Message: "A nota da avaliação deve estar entre 1 e 5."}
	}
	if dto.RideID == uuid.Nil || dto.PassengerID == uuid.Nil {
		return protocol.Response{Success: "error", Message: "Identificadores da carona e do passageiro são obrigatórios."}
	}

	ride, err := s.repository.GetRideByID(dto.RideID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Carona não encontrada."}
	}
	if ride.DriverID != driver.ID {
		return protocol.Response{Success: "error", Message: "A carona não pertence ao motorista autenticado."}
	}

	resID, segIDs, _, found := s.repository.HasConfirmedBookingForRide(dto.RideID, dto.PassengerID)
	if !found {
		return protocol.Response{Success: "error", Message: "O passageiro informado não possui reserva confirmada nesta carona."}
	}
	if dto.ReservationID == uuid.Nil {
		dto.ReservationID = resID
	}

	rideCompleted := ride.GetStatus() == models.RideStatusCompleted || ride.GetStatus() == models.RideStatusCompletedNoShow
	segmentCompleted := false
	for _, segID := range segIDs {
		if ride.IsSegmentCompleted(segID) {
			segmentCompleted = true
			break
		}
	}

	if !rideCompleted && !segmentCompleted {
		return protocol.Response{Success: "error", Message: "Aguarde concluir o trecho do passageiro ou a carona para avaliá-lo."}
	}

	if s.repository.HasRatingOfType(driver.ID, dto.RideID, dto.PassengerID, models.RatingTypeDriverToPassenger) {
		return protocol.Response{Success: "error", Message: "Você já avaliou este passageiro para esta carona."}
	}

	targetName := s.resolvePassengerNameForRide(dto.RideID, dto.PassengerID)

	rating := &models.Rating{
		ID:            uuid.New(),
		RideID:        dto.RideID,
		ReservationID: dto.ReservationID,
		AuthorID:      driver.ID,
		AuthorName:    driver.Name,
		TargetID:      dto.PassengerID,
		TargetName:    targetName,
		Type:          models.RatingTypeDriverToPassenger,
		Score:         dto.Score,
		Comment:       dto.Comment,
		CreatedAt:     time.Now(),
	}

	if err := s.repository.SaveRating(rating); err != nil {
		return protocol.Response{Success: "error", Message: "Falha ao registrar avaliação: " + err.Error()}
	}

	s.replicateRatingToPeers(rating)
	return reputationResponse(rating, "Avaliação do passageiro registrada com sucesso.")
}

func (s *Server) handleFileComplaint(payload json.RawMessage, session *Session) protocol.Response {
	if response, ok := s.passengerForSession(session); !ok {
		return response
	}

	var dto protocol.FileComplaintRequest
	if err := protocol.UnmarshalStrict(payload, &dto); err != nil {
		return protocol.Response{Success: "error", Message: "Requisição inválida: " + err.Error()}
	}

	comment := strings.TrimSpace(dto.Comment)
	if comment == "" {
		return protocol.Response{Success: "error", Message: "A descrição da denúncia é obrigatória."}
	}

	rideID, reservationID, targetID, targetName, errMsg := s.resolveComplaintContext(session.UserID, dto)
	if errMsg != "" {
		return protocol.Response{Success: "error", Message: errMsg}
	}

	if targetID == session.UserID {
		return protocol.Response{Success: "error", Message: "Não é possível registrar denúncia contra si mesmo."}
	}

	if s.repository.HasRatingOfType(session.UserID, rideID, targetID, models.RatingTypeComplaint) ||
		s.hasAggregatedRating(session.UserID, rideID, targetID, models.RatingTypeComplaint) {
		return protocol.Response{Success: "error", Message: "Você já registrou uma denúncia para este usuário nesta carona."}
	}

	complaint := &models.Rating{
		ID:            uuid.New(),
		RideID:        rideID,
		ReservationID: reservationID,
		AuthorID:      session.UserID,
		AuthorName:    session.UserName,
		TargetID:      targetID,
		TargetName:    targetName,
		Type:          models.RatingTypeComplaint,
		Score:         0,
		Comment:       comment,
		CreatedAt:     time.Now(),
	}

	if err := s.repository.SaveRating(complaint); err != nil {
		return protocol.Response{Success: "error", Message: "Falha ao registrar denúncia: " + err.Error()}
	}

	s.replicateRatingToPeers(complaint)
	return reputationResponse(complaint, "Denúncia registrada com sucesso.")
}

func (s *Server) handleGetUserReputation(payload json.RawMessage, session *Session) protocol.Response {
	var dto protocol.GetUserReputationRequest
	if len(payload) > 0 && string(payload) != "null" {
		if err := protocol.UnmarshalStrict(payload, &dto); err != nil {
			return protocol.Response{Success: "error", Message: "Requisição inválida: " + err.Error()}
		}
	}

	targetUserID := dto.UserID
	if targetUserID == uuid.Nil {
		if session != nil && session.IsAuthenticated() {
			targetUserID = session.UserID
		} else {
			return protocol.Response{Success: "error", Message: "Identificador de usuário não informado."}
		}
	}

	reputation, err := s.getAggregatedReputation(targetUserID)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Não foi possível obter a reputação: " + err.Error()}
	}

	return reputationResponse(reputation, "Reputação obtida com sucesso.")
}

func (s *Server) resolveRideFromReservation(
	reservation *models.Reservation,
	segmentID, rideID, driverID uuid.UUID,
) (*models.Ride, models.ReservedSegment, string) {
	type resolvedCandidate struct {
		seg  models.ReservedSegment
		ride *models.Ride
	}

	var candidates []resolvedCandidate
	distinctRides := make(map[uuid.UUID]struct{})

	for _, seg := range reservation.Segments {
		if segmentID != uuid.Nil && seg.SegmentID != segmentID {
			continue
		}
		if rideID != uuid.Nil && seg.RideID != rideID {
			continue
		}

		compID := seg.CompanyID
		if compID == "" {
			compID = s.companyID
		}
		ride, err := s.getRideLocalOrRemote(compID, seg.RideID)
		if err != nil {
			continue
		}
		if driverID != uuid.Nil && ride.DriverID != driverID {
			continue
		}
		distinctRides[ride.ID] = struct{}{}
		candidates = append(candidates, resolvedCandidate{seg: seg, ride: ride})
	}

	if len(candidates) == 0 {
		return nil, models.ReservedSegment{}, "Carona correspondente à reserva não encontrada no consórcio."
	}

	if segmentID == uuid.Nil && rideID == uuid.Nil && driverID == uuid.Nil && len(distinctRides) > 1 {
		return nil, models.ReservedSegment{}, "Esta reserva possui múltiplas caronas. Informe qual trecho ou motorista deseja avaliar."
	}

	return candidates[0].ride, candidates[0].seg, ""
}

func (s *Server) isReservationPartCompleted(
	reservation *models.Reservation,
	targetRide *models.Ride,
	selectedSeg models.ReservedSegment,
) bool {
	if selectedSeg.SegmentID != uuid.Nil {
		if targetRide.IsSegmentCompleted(selectedSeg.SegmentID) {
			return true
		}
	}
	for _, seg := range reservation.Segments {
		if seg.RideID == targetRide.ID && targetRide.IsSegmentCompleted(seg.SegmentID) {
			return true
		}
	}
	return false
}

func (s *Server) resolveComplaintContext(
	authorID uuid.UUID,
	dto protocol.FileComplaintRequest,
) (rideID, reservationID, targetID uuid.UUID, targetName, errMsg string) {
	if dto.ReservationID != uuid.Nil {
		reservation, err := s.repository.GetReservationByID(dto.ReservationID)
		if err != nil {
			return uuid.Nil, uuid.Nil, uuid.Nil, "", "Reserva não encontrada."
		}
		if reservation.PassengerID != authorID {
			return uuid.Nil, uuid.Nil, uuid.Nil, "", "A reserva informada não pertence ao usuário autenticado."
		}
		if reservation.Status != enum.Confirmada {
			return uuid.Nil, uuid.Nil, uuid.Nil, "", "Apenas reservas confirmadas permitem registrar denúncia."
		}
		ride, _, msg := s.resolveRideFromReservation(reservation, uuid.Nil, dto.RideID, dto.TargetID)
		if msg != "" {
			return uuid.Nil, uuid.Nil, uuid.Nil, "", msg
		}
		return ride.ID, reservation.ID, ride.DriverID, ride.DriverName, ""
	}

	if dto.RideID == uuid.Nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, "", "Informe reservation_id ou ride_id para comprovar participação na viagem."
	}

	// Verifica se o autor é o motorista local desta carona denunciando um passageiro
	if localRide, err := s.repository.GetRideByID(dto.RideID); err == nil && localRide.DriverID == authorID {
		if dto.TargetID == uuid.Nil {
			return uuid.Nil, uuid.Nil, uuid.Nil, "", "Informe target_id do passageiro a ser denunciado."
		}
		resID, _, _, found := s.repository.HasConfirmedBookingForRide(dto.RideID, dto.TargetID)
		if !found {
			return uuid.Nil, uuid.Nil, uuid.Nil, "", "O usuário informado não possui reserva confirmada nesta carona."
		}
		return dto.RideID, resID, dto.TargetID, s.resolvePassengerNameForRide(dto.RideID, dto.TargetID), ""
	}

	// Caso contrário, verifica se o usuário autenticado é passageiro de alguma reserva confirmada nessa carona
	reservations, err := s.repository.GetReservationsByPassengerID(authorID)
	if err == nil {
		for _, res := range reservations {
			if res.Status != enum.Confirmada {
				continue
			}
			for _, seg := range res.Segments {
				if seg.RideID == dto.RideID {
					compID := seg.CompanyID
					if compID == "" {
						compID = s.companyID
					}
					ride, rErr := s.getRideLocalOrRemote(compID, seg.RideID)
					if rErr == nil && (dto.TargetID == uuid.Nil || dto.TargetID == ride.DriverID) {
						return ride.ID, res.ID, ride.DriverID, ride.DriverName, ""
					}
				}
			}
		}
	}

	return uuid.Nil, uuid.Nil, uuid.Nil, "", "Você não possui participação confirmada nesta carona para registrar denúncia."
}

func (s *Server) getRideLocalOrRemote(companyID string, rideID uuid.UUID) (*models.Ride, error) {
	if ride, err := s.repository.GetRideByID(rideID); err == nil {
		return ride, nil
	}
	if s.peers == nil {
		return nil, errors.New("registro de parceiros indisponível")
	}

	if companyID != "" && companyID != s.companyID {
		peer, exists := s.peers.Get(companyID)
		if !exists {
			return nil, fmt.Errorf("empresa %s não encontrada", companyID)
		}
		return s.fetchRemoteRide(peer.InternalURL, rideID)
	}

	for _, peer := range s.peers.All() {
		if ride, err := s.fetchRemoteRide(peer.InternalURL, rideID); err == nil {
			return ride, nil
		}
	}

	return nil, errors.New("carona não encontrada")
}

func (s *Server) replicateRatingToPeers(rating *models.Rating) {
	if s.peers == nil || rating == nil {
		return
	}
	body, err := json.Marshal(rating)
	if err != nil {
		return
	}

	for _, peer := range s.peers.All() {
		url := fmt.Sprintf("%s/internal/v1/reputation/records", peer.InternalURL)
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}
}

func (s *Server) getAggregatedReputation(userID uuid.UUID) (*models.UserReputation, error) {
	localRep, err := s.repository.GetReputationByUserID(userID)
	if err != nil {
		return nil, err
	}

	userName := localRep.UserName
	allRatings := make([]models.Rating, len(localRep.Ratings))
	copy(allRatings, localRep.Ratings)

	if s.peers != nil {
		for _, peer := range s.peers.All() {
			url := fmt.Sprintf("%s/internal/v1/reputation/%s", peer.InternalURL, userID)
			req, reqErr := http.NewRequest(http.MethodGet, url, nil)
			if reqErr != nil {
				continue
			}
			resp, respErr := s.httpClient.Do(req)
			if respErr != nil {
				continue
			}
			var remoteRep models.UserReputation
			if resp.StatusCode == http.StatusOK {
				if decErr := json.NewDecoder(resp.Body).Decode(&remoteRep); decErr == nil {
					if userName == "" && remoteRep.UserName != "" {
						userName = remoteRep.UserName
					}
					allRatings = append(allRatings, remoteRep.Ratings...)
				}
			}
			_ = resp.Body.Close()
		}
	}

	return aggregateUserReputation(userID, userName, allRatings), nil
}

func (s *Server) hasPassengerNoShow(rideID, passengerID uuid.UUID) bool {
	if s.repository.HasNoShowForRide(rideID, passengerID) {
		return true
	}
	rep, err := s.getAggregatedReputation(passengerID)
	if err != nil || rep == nil {
		return false
	}
	for _, r := range rep.Ratings {
		if r.RideID == rideID && r.Type == models.RatingTypeNoShow {
			return true
		}
	}
	return false
}

func (s *Server) hasAggregatedRating(authorID, rideID, targetID uuid.UUID, ratingType models.RatingType) bool {
	rep, err := s.getAggregatedReputation(targetID)
	if err != nil || rep == nil {
		return false
	}
	for _, r := range rep.Ratings {
		if r.AuthorID == authorID && r.RideID == rideID && r.TargetID == targetID && r.Type == ratingType {
			return true
		}
	}
	return false
}

func reputationResponse(data any, message string) protocol.Response {
	payload, err := json.Marshal(data)
	if err != nil {
		return protocol.Response{Success: "error", Message: "Não foi possível processar a resposta."}
	}

	return protocol.Response{Success: "success", Message: message, Payload: payload}
}
