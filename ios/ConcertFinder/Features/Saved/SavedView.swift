import Observation
import SwiftUI

@MainActor
@Observable
final class SavedModel {
    private(set) var events: [Event] = []
    private(set) var isLoading = false
    private(set) var error: APIError?

    private let api: APIClient

    init(api: APIClient) {
        self.api = api
    }

    /// Past shows are already floored out server-side, at the start of the
    /// current UTC day — the same rule the feed uses. Nothing to filter here.
    func load() async {
        isLoading = events.isEmpty
        do {
            events = try await api.savedConcerts().events
            error = nil
        } catch let apiError as APIError {
            error = apiError
        } catch {
            self.error = .unknown(error.localizedDescription)
        }
        isLoading = false
    }

    /// Unsave is by dedup_key in the path — per act, not per card.
    func unsave(act: Act) async {
        let previous = events
        removeAct(dedupKey: act.dedupKey)
        do {
            try await api.unsave(dedupKey: act.dedupKey)
        } catch {
            events = previous
            self.error = error as? APIError ?? .unknown(error.localizedDescription)
        }
    }

    /// Dropping an act can empty its card, in which case the card goes too —
    /// an event with no saved acts is not a saved event.
    private func removeAct(dedupKey: String) {
        for i in events.indices {
            events[i].acts.removeAll { $0.dedupKey == dedupKey }
        }
        events.removeAll { $0.acts.isEmpty }
    }
}

struct SavedView: View {
    @Environment(SavedModel.self) private var model

    var body: some View {
        NavigationStack {
            Group {
                if model.isLoading && model.events.isEmpty {
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if model.events.isEmpty {
                    ContentUnavailableView {
                        Label("Nothing saved", systemImage: "bookmark")
                    } description: {
                        Text("Tap the bookmark on any show to keep it here.")
                    }
                } else {
                    list
                }
            }
            .background(Color.screenBackground)
            .navigationTitle("Saved")
            .refreshable { await model.load() }
            .task { await model.load() }
            .navigationDestination(for: Event.self) { EventDetailView(event: $0) }
        }
    }

    private var list: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Metrics.cardSpacing) {
                ForEach(model.events) { event in
                    NavigationLink(value: event) {
                        SavedCard(event: event) { act in
                            Task { await model.unsave(act: act) }
                        }
                    }
                    .buttonStyle(.plain)
                }
                SpotifyAttribution()
                    .frame(maxWidth: .infinity, alignment: .center)
                    .padding(.top, Metrics.loose)
            }
            .padding(Metrics.gutter)
        }
    }
}

private struct SavedCard: View {
    let event: Event
    var onUnsave: (Act) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Metrics.tight) {
            VStack(alignment: .leading, spacing: 2) {
                Text(event.venue).font(.headline)
                Text(event.location).font(.subheadline).foregroundStyle(.secondary)
                Text(event.date.formatted(date: .abbreviated, time: .shortened))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Divider()
            ForEach(event.acts) { act in
                HStack {
                    Text(act.artist.name).font(.subheadline.weight(.medium))
                    Spacer()
                    Button {
                        onUnsave(act)
                    } label: {
                        Image(systemName: "bookmark.fill")
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Color.accentColor)
                    .accessibilityLabel("Remove \(act.artist.name) from saved")
                }
                .contentShape(Rectangle())
            }
        }
        .padding(Metrics.gutter)
        .background(Color.cardBackground)
        .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
    }
}
