package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/models"
	"github.com/JoaoAnunciacaoDev/PBL---VaiJunto-Concorrencia-E-Conectividade/internal/protocol"
	"github.com/google/uuid"
)

func startRide(tcpClient *TCPClient, input *bufio.Reader, output io.Writer) bool {
	rides, ok := getMyRides(tcpClient, output)
	if !ok {
		return false
	}

	var scheduled []models.Ride
	for _, r := range rides {
		if r.GetStatus() == models.RideStatusScheduled && !r.Cancelled {
			scheduled = append(scheduled, r)
		}
	}

	if len(scheduled) == 0 {
		fmt.Fprintln(output, "Nenhuma carona agendada elegível para início.")
		return true
	}

	fmt.Fprintln(output, "\n=== Iniciar Carona ===")
	for index, r := range scheduled {
		orig := r.Segments[0].Origin.String()
		dest := r.Segments[len(r.Segments)-1].Destination.String()
		fmt.Fprintf(output, "%d - %s → %s | Saída: %s\n",
			index+1, orig, dest, r.DepartureAt.Format(rideDateTimeLayout))
	}

	choice, ok := chooseNumber(input, output, "Escolha a carona para iniciar ou 0 para voltar: ", len(scheduled))
	if !ok || choice == 0 {
		return true
	}

	selected := scheduled[choice-1]
	payload, _ := json.Marshal(protocol.StartRideRequest{RideID: selected.ID})

	resp, err := tcpClient.Send(protocol.Request{
		Action:  protocol.ActionStartRide,
		Payload: payload,
	})
	if err != nil {
		fmt.Fprintf(output, "Erro de comunicação: %v\n", err)
		return false
	}

	fmt.Fprintln(output, resp.Message)
	return true
}

func finishRide(tcpClient *TCPClient, input *bufio.Reader, output io.Writer) bool {
	rides, ok := getMyRides(tcpClient, output)
	if !ok {
		return false
	}

	var inProgress []models.Ride
	for _, r := range rides {
		if r.GetStatus() == models.RideStatusInProgress {
			inProgress = append(inProgress, r)
		}
	}

	if len(inProgress) == 0 {
		fmt.Fprintln(output, "Nenhuma carona em andamento para finalizar.")
		return true
	}

	fmt.Fprintln(output, "\n=== Concluir Carona Completa ===")
	for index, r := range inProgress {
		orig := r.Segments[0].Origin.String()
		dest := r.Segments[len(r.Segments)-1].Destination.String()
		fmt.Fprintf(output, "%d - %s → %s | Iniciada em: %s\n",
			index+1, orig, dest, r.DepartureAt.Format(rideDateTimeLayout))
	}

	choice, ok := chooseNumber(input, output, "Escolha a carona para finalizar ou 0 para voltar: ", len(inProgress))
	if !ok || choice == 0 {
		return true
	}

	selected := inProgress[choice-1]
	payload, _ := json.Marshal(protocol.FinishRideRequest{RideID: selected.ID})

	resp, err := tcpClient.Send(protocol.Request{
		Action:  protocol.ActionFinishRide,
		Payload: payload,
	})
	if err != nil {
		fmt.Fprintf(output, "Erro de comunicação: %v\n", err)
		return false
	}

	fmt.Fprintln(output, resp.Message)
	return true
}

func finishSegment(tcpClient *TCPClient, input *bufio.Reader, output io.Writer) bool {
	rides, ok := getMyRides(tcpClient, output)
	if !ok {
		return false
	}

	var activeRides []models.Ride
	for _, r := range rides {
		if !r.Cancelled && r.GetStatus() != models.RideStatusCompleted && r.GetStatus() != models.RideStatusCompletedNoShow {
			activeRides = append(activeRides, r)
		}
	}

	if len(activeRides) == 0 {
		fmt.Fprintln(output, "Nenhuma carona com trechos pendentes para concluir.")
		return true
	}

	fmt.Fprintln(output, "\n=== Concluir Trecho de Carona ===")
	for index, r := range activeRides {
		orig := r.Segments[0].Origin.String()
		dest := r.Segments[len(r.Segments)-1].Destination.String()
		fmt.Fprintf(output, "%d - %s → %s | Status: %s (%d trecho(s))\n",
			index+1, orig, dest, r.GetStatus(), len(r.Segments))
	}

	choice, ok := chooseNumber(input, output, "Escolha a carona ou 0 para voltar: ", len(activeRides))
	if !ok || choice == 0 {
		return true
	}
	selectedRide := activeRides[choice-1]

	var pendingSegments []models.Stage
	for _, s := range selectedRide.Segments {
		if s.GetStatus() != models.StageStatusCompleted {
			pendingSegments = append(pendingSegments, s)
		}
	}

	if len(pendingSegments) == 0 {
		fmt.Fprintln(output, "Todos os trechos desta carona já foram concluídos.")
		return true
	}

	fmt.Fprintln(output, "\nTrechos pendentes:")
	for index, s := range pendingSegments {
		fmt.Fprintf(output, "%d - %s → %s | Partida: %s\n",
			index+1, s.Origin, s.Destination, s.DepartureAt.Format(rideDateTimeLayout))
	}

	segChoice, ok := chooseNumber(input, output, "Escolha o trecho para concluir ou 0 para voltar: ", len(pendingSegments))
	if !ok || segChoice == 0 {
		return true
	}
	selectedSeg := pendingSegments[segChoice-1]

	payload, _ := json.Marshal(protocol.FinishSegmentRequest{
		RideID:    selectedRide.ID,
		SegmentID: selectedSeg.ID,
	})

	resp, err := tcpClient.Send(protocol.Request{
		Action:  protocol.ActionFinishSegment,
		Payload: payload,
	})
	if err != nil {
		fmt.Fprintf(output, "Erro de comunicação: %v\n", err)
		return false
	}

	fmt.Fprintln(output, resp.Message)
	return true
}

func reportNoShow(tcpClient *TCPClient, input *bufio.Reader, output io.Writer) bool {
	rides, ok := getMyRides(tcpClient, output)
	if !ok {
		return false
	}

	var activeRides []models.Ride
	for _, r := range rides {
		st := r.GetStatus()
		if (st == models.RideStatusScheduled || st == models.RideStatusInProgress) && !r.Cancelled {
			activeRides = append(activeRides, r)
		}
	}

	if len(activeRides) == 0 {
		fmt.Fprintln(output, "Nenhuma carona ativa encontrada.")
		return true
	}

	fmt.Fprintln(output, "\n=== Reportar Ausência (No-Show) ===")
	for index, r := range activeRides {
		orig := r.Segments[0].Origin.String()
		dest := r.Segments[len(r.Segments)-1].Destination.String()
		fmt.Fprintf(output, "%d - %s → %s | Status: %s\n", index+1, orig, dest, r.GetStatus())
	}

	choice, ok := chooseNumber(input, output, "Escolha a carona ou 0 para voltar: ", len(activeRides))
	if !ok || choice == 0 {
		return true
	}
	selectedRide := activeRides[choice-1]

	paxResp, ok := fetchRidePassengers(tcpClient, selectedRide.ID, output)
	if !ok {
		return false
	}

	var allPassengers []protocol.UserResponse
	seen := make(map[uuid.UUID]bool)
	for _, seg := range paxResp.Segments {
		for _, p := range seg.Passengers {
			if !seen[p.ID] {
				seen[p.ID] = true
				allPassengers = append(allPassengers, p)
			}
		}
	}

	if len(allPassengers) == 0 {
		fmt.Fprintln(output, "Nenhum passageiro confirmado encontrado nesta carona.")
		return true
	}

	fmt.Fprintln(output, "\nPassageiros da Carona:")
	for index, p := range allPassengers {
		fmt.Fprintf(output, "%d - %s (%s)\n", index+1, p.Name, p.Email)
	}

	paxChoice, ok := chooseNumber(input, output, "Escolha o passageiro ausente ou 0 para voltar: ", len(allPassengers))
	if !ok || paxChoice == 0 {
		return true
	}
	selectedPax := allPassengers[paxChoice-1]

	comment, err := readLine(input, output, "Motivo/Observação da ausência: ")
	if err != nil {
		return true
	}

	payload, _ := json.Marshal(protocol.ReportNoShowRequest{
		RideID:      selectedRide.ID,
		PassengerID: selectedPax.ID,
		Comment:     comment,
	})

	resp, err := tcpClient.Send(protocol.Request{
		Action:  protocol.ActionReportNoShow,
		Payload: payload,
	})
	if err != nil {
		fmt.Fprintf(output, "Erro de comunicação: %v\n", err)
		return false
	}

	fmt.Fprintln(output, resp.Message)
	return true
}

func ratePassenger(tcpClient *TCPClient, input *bufio.Reader, output io.Writer) bool {
	rides, ok := getMyRides(tcpClient, output)
	if !ok {
		return false
	}

	var completed []models.Ride
	for _, r := range rides {
		st := r.GetStatus()
		if st == models.RideStatusCompleted || st == models.RideStatusCompletedNoShow {
			completed = append(completed, r)
		}
	}

	if len(completed) == 0 {
		fmt.Fprintln(output, "Nenhuma carona finalizada elegível para avaliação de passageiros.")
		return true
	}

	fmt.Fprintln(output, "\n=== Avaliar Passageiro ===")
	for index, r := range completed {
		orig := r.Segments[0].Origin.String()
		dest := r.Segments[len(r.Segments)-1].Destination.String()
		fmt.Fprintf(output, "%d - %s → %s | Data: %s\n",
			index+1, orig, dest, r.DepartureAt.Format(rideDateTimeLayout))
	}

	choice, ok := chooseNumber(input, output, "Escolha a carona finalizada ou 0 para voltar: ", len(completed))
	if !ok || choice == 0 {
		return true
	}
	selectedRide := completed[choice-1]

	paxResp, ok := fetchRidePassengers(tcpClient, selectedRide.ID, output)
	if !ok {
		return false
	}

	var allPassengers []protocol.UserResponse
	seen := make(map[uuid.UUID]bool)
	for _, seg := range paxResp.Segments {
		for _, p := range seg.Passengers {
			if !seen[p.ID] {
				seen[p.ID] = true
				allPassengers = append(allPassengers, p)
			}
		}
	}

	if len(allPassengers) == 0 {
		fmt.Fprintln(output, "Nenhum passageiro encontrado nesta carona.")
		return true
	}

	fmt.Fprintln(output, "\nPassageiros:")
	for index, p := range allPassengers {
		fmt.Fprintf(output, "%d - %s (%s)\n", index+1, p.Name, p.Email)
	}

	paxChoice, ok := chooseNumber(input, output, "Escolha o passageiro para avaliar ou 0 para voltar: ", len(allPassengers))
	if !ok || paxChoice == 0 {
		return true
	}
	selectedPax := allPassengers[paxChoice-1]

	score, ok := chooseNumber(input, output, "Nota de 1 a 5: ", 5)
	if !ok || score < 1 {
		return true
	}

	comment, err := readLine(input, output, "Comentário sobre o passageiro: ")
	if err != nil {
		return true
	}

	payload, _ := json.Marshal(protocol.RatePassengerRequest{
		RideID:      selectedRide.ID,
		PassengerID: selectedPax.ID,
		Score:       score,
		Comment:     comment,
	})

	resp, err := tcpClient.Send(protocol.Request{
		Action:  protocol.ActionRatePassenger,
		Payload: payload,
	})
	if err != nil {
		fmt.Fprintf(output, "Erro de comunicação: %v\n", err)
		return false
	}

	fmt.Fprintln(output, resp.Message)
	return true
}

func rateDriver(tcpClient *TCPClient, input *bufio.Reader, output io.Writer) bool {
	reservations, ok := getMyReservations(tcpClient, output)
	if !ok {
		return false
	}

	if len(reservations) == 0 {
		fmt.Fprintln(output, "Nenhuma reserva encontrada.")
		return true
	}

	fmt.Fprintln(output, "\n=== Avaliar Motorista ===")
	for index, res := range reservations {
		fmt.Fprintf(output, "%d - Reserva %s | %d trecho(s) | Status: %s\n",
			index+1, res.ID.String()[:8], len(res.Segments), res.Status)
	}

	choice, ok := chooseNumber(input, output, "Escolha a reserva ou 0 para voltar: ", len(reservations))
	if !ok || choice == 0 {
		return true
	}
	selectedRes := reservations[choice-1]

	score, ok := chooseNumber(input, output, "Nota de 1 a 5 para a viagem: ", 5)
	if !ok || score < 1 {
		return true
	}

	comment, err := readLine(input, output, "Comentário sobre a viagem e motorista: ")
	if err != nil {
		return true
	}

	payload, _ := json.Marshal(protocol.RateDriverRequest{
		ReservationID: selectedRes.ID,
		Score:         score,
		Comment:       comment,
	})

	resp, err := tcpClient.Send(protocol.Request{
		Action:  protocol.ActionRateDriver,
		Payload: payload,
	})
	if err != nil {
		fmt.Fprintf(output, "Erro de comunicação: %v\n", err)
		return false
	}

	fmt.Fprintln(output, resp.Message)
	return true
}

func viewReputation(tcpClient *TCPClient, input *bufio.Reader, output io.Writer) bool {
	fmt.Fprintln(output, "\n=== Consulta de Reputação e Histórico ===")
	targetIDStr, err := readLine(input, output, "UUID do usuário (deixe vazio para ver a sua): ")
	if err != nil {
		return true
	}

	var req protocol.GetUserReputationRequest
	targetIDStr = strings.TrimSpace(targetIDStr)
	if targetIDStr != "" {
		parsed, pErr := uuid.Parse(targetIDStr)
		if pErr != nil {
			fmt.Fprintln(output, "UUID inválido.")
			return true
		}
		req.UserID = parsed
	}

	payload, _ := json.Marshal(req)
	resp, err := tcpClient.Send(protocol.Request{
		Action:  protocol.ActionGetUserReputation,
		Payload: payload,
	})
	if err != nil {
		fmt.Fprintf(output, "Erro de comunicação: %v\n", err)
		return false
	}

	if resp.Success != "success" {
		fmt.Fprintf(output, "Erro: %s\n", resp.Message)
		return true
	}

	var rep models.UserReputation
	if err := json.Unmarshal(resp.Payload, &rep); err != nil {
		fmt.Fprintf(output, "Falha ao processar resposta: %v\n", err)
		return true
	}

	fmt.Fprintf(output, "\n--- Reputação de: %s (%s) ---\n", rep.UserName, rep.UserID)
	fmt.Fprintf(output, "Média de Avaliações: %.2f / 5.0 (Total: %d)\n", rep.AverageScore, rep.TotalRatings)
	fmt.Fprintf(output, "Ocorrências de No-Show: %d\n", rep.NoShowCount)
	fmt.Fprintf(output, "Cancelamentos Tardios: %d\n", rep.LateCancelledCount)

	if len(rep.Ratings) > 0 {
		fmt.Fprintln(output, "\nHistórico de Registros:")
		for idx, r := range rep.Ratings {
			scoreStr := fmt.Sprintf("%d/5", r.Score)
			if r.Type == models.RatingTypeNoShow {
				scoreStr = "[OCORRÊNCIA NO-SHOW]"
			}
			fmt.Fprintf(output, "  %d. %s por %s | %s\n     \"%s\" (%s)\n",
				idx+1, r.Type, r.AuthorName, scoreStr, r.Comment, r.CreatedAt.Format("02/01/2006 15:04"))
		}
	} else {
		fmt.Fprintln(output, "\nNenhum registro de avaliação até o momento.")
	}

	return true
}

func fetchRidePassengers(tcpClient *TCPClient, rideID uuid.UUID, output io.Writer) (*protocol.RidePassengersResponse, bool) {
	payload, _ := json.Marshal(protocol.GetRidePassengersRequest{RideID: rideID})
	resp, err := tcpClient.Send(protocol.Request{
		Action:  protocol.ActionGetRidePassengers,
		Payload: payload,
	})
	if err != nil {
		fmt.Fprintf(output, "Erro de comunicação: %v\n", err)
		return nil, false
	}
	if resp.Success != "success" {
		fmt.Fprintf(output, "Erro: %s\n", resp.Message)
		return nil, true
	}

	var data protocol.RidePassengersResponse
	if err := json.Unmarshal(resp.Payload, &data); err != nil {
		return nil, true
	}
	return &data, true
}
