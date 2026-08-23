import EventKit
import SafariServices
import SwiftUI
import UIKit

/// One show in full: every act with its own save/subscribe, ticket links, and
/// the two system integrations a concert app is expected to have.
///
/// Built entirely from the Event already in hand — there is no detail
/// endpoint, and adding one would spend a round trip on data the feed
/// response already carried.
struct EventDetailView: View {
    @State private var event: Event
    @Environment(FeedModel.self) private var model
    @State private var safariURL: URL?
    @State private var calendarError: String?

    init(event: Event) {
        _event = State(initialValue: event)
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Metrics.loose) {
                header
                lineup
                if !event.links.isEmpty { tickets }
                actions
                SpotifyAttribution()
                    .frame(maxWidth: .infinity, alignment: .center)
            }
            .padding(Metrics.gutter)
        }
        .background(Color.screenBackground)
        .navigationTitle(event.acts.first?.artist.name ?? event.venue)
        .navigationBarTitleDisplayMode(.inline)
        .sheet(item: $safariURL) { url in
            SafariView(url: url)
                .ignoresSafeArea()
        }
        .alert("Couldn't add to Calendar", isPresented: .constant(calendarError != nil)) {
            Button("OK") { calendarError = nil }
        } message: {
            Text(calendarError ?? "")
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(event.venue).font(.title2.weight(.semibold))
            Text(event.location).font(.subheadline).foregroundStyle(.secondary)
            // Only claim a time when there is one act. On a bill the date is
            // the earliest set time, which is not when any given act plays.
            Text(event.acts.count == 1
                 ? event.date.formatted(date: .complete, time: .shortened)
                 : event.date.formatted(date: .complete, time: .omitted))
                .font(.subheadline)
                .foregroundStyle(.secondary)
        }
    }

    private var lineup: some View {
        VStack(alignment: .leading, spacing: Metrics.tight) {
            Text(event.acts.count == 1 ? "Artist" : "From your library")
                .font(.headline)
            ForEach(event.acts) { act in
                ActRow(
                    act: act,
                    onToggleSave: {
                        toggleLocally(dedupKey: act.dedupKey)
                        Task { await model.toggleSave(act: act) }
                    },
                    onToggleSubscribe: {
                        toggleSubscribeLocally(artistID: act.artist.id)
                        Task { await model.toggleSubscribe(act: act) }
                    }
                )
                .padding(.vertical, 4)
                if act.id != event.acts.last?.id { Divider() }
            }
        }
        .padding(Metrics.gutter)
        .background(Color.cardBackground)
        .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
    }

    private var tickets: some View {
        VStack(alignment: .leading, spacing: Metrics.tight) {
            Text("Tickets").font(.headline)
            ForEach(event.links, id: \.url) { link in
                if let url = link.url {
                    Button {
                        // SFSafariViewController, not an external jump: it
                        // keeps the user in the app and does not raise the
                        // in-app-purchase question a native checkout would.
                        safariURL = url
                    } label: {
                        HStack {
                            Text(link.label)
                            Spacer()
                            Image(systemName: "arrow.up.right.square")
                                .foregroundStyle(.secondary)
                        }
                    }
                    .buttonStyle(.plain)
                    .padding(.vertical, 6)
                    if link.url != event.links.last?.url { Divider() }
                }
            }
        }
        .padding(Metrics.gutter)
        .background(Color.cardBackground)
        .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
    }

    private var actions: some View {
        VStack(spacing: Metrics.tight) {
            Button {
                Task { await addToCalendar() }
            } label: {
                Label("Add to Calendar", systemImage: "calendar.badge.plus")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.bordered)

            Button {
                openInMaps()
            } label: {
                Label("Directions to \(event.venue)", systemImage: "map")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.bordered)
        }
    }

    // MARK: - Local optimistic state

    /// The detail view holds its own copy of the event so its controls
    /// respond immediately. The model is the source of truth and is updated
    /// alongside; this only keeps the visible screen honest while that
    /// round trip is in flight.
    private func toggleLocally(dedupKey: String) {
        for i in event.acts.indices where event.acts[i].dedupKey == dedupKey {
            event.acts[i].saved = !(event.acts[i].saved ?? false)
        }
    }

    private func toggleSubscribeLocally(artistID: String) {
        for i in event.acts.indices where event.acts[i].artist.id == artistID {
            event.acts[i].subscribed = !(event.acts[i].subscribed ?? false)
        }
    }

    // MARK: - System integrations

    private func addToCalendar() async {
        let store = EKEventStore()
        do {
            guard try await store.requestWriteOnlyAccessToEvents() else {
                calendarError = "ConcertFinder doesn't have permission to add events. You can grant it in Settings."
                return
            }
            let calendarEvent = EKEvent(eventStore: store)
            calendarEvent.title = event.acts.map(\.artist.name).joined(separator: ", ")
            calendarEvent.startDate = event.date
            // Doors-to-late is a guess whatever we pick; 3 hours is the
            // conventional slot and the user can edit it.
            calendarEvent.endDate = event.date.addingTimeInterval(3 * 60 * 60)
            calendarEvent.location = "\(event.venue), \(event.location)"
            calendarEvent.calendar = store.defaultCalendarForNewEvents
            try store.save(calendarEvent, span: .thisEvent)
        } catch {
            calendarError = error.localizedDescription
        }
    }

    private func openInMaps() {
        var components = URLComponents(string: "https://maps.apple.com/")
        var items = [URLQueryItem(name: "q", value: "\(event.venue), \(event.location)")]
        // Most events carry venue coordinates. Passing them drops an exact
        // pin instead of making Maps search a venue name, which is ambiguous
        // for chains ("The Fillmore") and multi-room complexes. The name is
        // still sent as `q` so the pin is labelled rather than anonymous.
        //
        // They are omitempty on the wire, so treat them as optional and fall
        // back to the search — a wrong pin is worse than letting Maps look.
        if let latitude = event.latitude, let longitude = event.longitude,
           latitude != 0 || longitude != 0 {
            items.append(URLQueryItem(name: "ll", value: "\(latitude),\(longitude)"))
        }
        components?.queryItems = items
        if let url = components?.url {
            UIApplication.shared.open(url)
        }
    }
}

/// SFSafariViewController wrapper. Ticket links open here rather than in
/// Safari so the user keeps their place in the app.
struct SafariView: UIViewControllerRepresentable {
    let url: URL

    func makeUIViewController(context: Context) -> SFSafariViewController {
        SFSafariViewController(url: url)
    }

    func updateUIViewController(_ controller: SFSafariViewController, context: Context) {}
}

/// Lets a bare URL drive `.sheet(item:)`.
extension URL: @retroactive Identifiable {
    public var id: String { absoluteString }
}
