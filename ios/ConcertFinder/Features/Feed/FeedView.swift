import SwiftUI

struct FeedView: View {
    @Environment(FeedModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @State private var showingFilters = false

    var body: some View {
        @Bindable var model = model

        NavigationStack {
            Group {
                if model.isLoading && model.events.isEmpty {
                    ProgressView("Finding concerts near you…")
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if model.isEmpty {
                    emptyState
                } else {
                    list
                }
            }
            .background(Color.screenBackground)
            .navigationTitle("Concerts")
            .toolbar { toolbar }
            .refreshable { await model.refresh() }
            .sheet(isPresented: $showingFilters) {
                FiltersSheet(filters: $model.filters, facets: model.facets)
            }
            .task { await model.start() }
            .onChange(of: scenePhase) { _, phase in
                // A 10-second timer that survives backgrounding is a battery
                // complaint and an App Review question.
                switch phase {
                case .active: model.resumePollingIfNeeded()
                case .background, .inactive: model.suspendPolling()
                @unknown default: break
                }
            }
        }
    }

    private var list: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Metrics.cardSpacing, pinnedViews: [.sectionHeaders]) {
                banners

                ForEach(model.sections, id: \.key) { section in
                    Section {
                        ForEach(section.events) { event in
                            NavigationLink(value: event) {
                                EventCard(
                                    event: event,
                                    onToggleSave: { act in Task { await model.toggleSave(act: act) } },
                                    onToggleSubscribe: { act in Task { await model.toggleSubscribe(act: act) } }
                                )
                            }
                            .buttonStyle(.plain)
                        }
                    } header: {
                        Text(section.heading)
                            .font(.subheadline.weight(.semibold))
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.vertical, 6)
                            .background(Color.screenBackground)
                    }
                }

                SpotifyAttribution()
                    .frame(maxWidth: .infinity, alignment: .center)
                    .padding(.top, Metrics.loose)
            }
            .padding(Metrics.gutter)
        }
        .navigationDestination(for: Event.self) { event in
            EventDetailView(event: event)
        }
        .navigationDestination(for: FeedRoute.self) { route in
            switch route {
            case .location: LocationView()
            }
        }
    }

    @ViewBuilder
    private var banners: some View {
        // `complete: false` is a UI state, not a log line — a quiet week and
        // a truncated scan are indistinguishable without saying so.
        if !model.complete {
            if let retryAfter = model.retryAfter {
                InfoBanner(kind: .quotaExhausted(until: retryAfter))
            } else {
                InfoBanner(kind: .incomplete)
            }
        }
        if model.isShowingCachedData {
            InfoBanner(kind: .offline(since: model.computedAt))
        }
        if model.isRefreshing {
            HStack(spacing: Metrics.tight) {
                ProgressView().controlSize(.small)
                Text("Checking for new shows…")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        // A location the user never set is the deployment's fallback, so
        // prompt rather than silently showing someone else's city.
        if let location = model.location, location.usesFallback {
            NavigationLink(value: FeedRoute.location) {
                HStack(spacing: Metrics.tight) {
                    Image(systemName: "location.slash")
                        .foregroundStyle(.secondary)
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Set your location")
                            .font(.footnote.weight(.semibold))
                        Text("These are shows near a default city, not near you.")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer(minLength: 0)
                    Image(systemName: "chevron.right")
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                }
                .padding(Metrics.gutter)
                .background(Color(.tertiarySystemFill))
                .clipShape(RoundedRectangle(cornerRadius: Metrics.cardRadius, style: .continuous))
            }
            .buttonStyle(.plain)
        }
    }

    private var emptyState: some View {
        ContentUnavailableView {
            Label("No concerts yet", systemImage: "music.mic")
        } description: {
            if !model.complete {
                Text("We couldn't check every artist in your profile. Pull to refresh, or widen your search radius.")
            } else if model.filters.activeCount > 0 {
                Text("No shows match your filters. Try clearing them.")
            } else {
                Text("Nothing coming up near you yet. We check daily and will notify you when something lands.")
            }
        } actions: {
            if model.filters.activeCount > 0 {
                Button("Clear filters") { model.filters = .empty }
            }
        }
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .topBarLeading) {
            if let computedAt = model.computedAt {
                Text("Updated \(computedAt.relativeDescription)")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button {
                showingFilters = true
            } label: {
                Label("Filters", systemImage: model.filters.activeCount > 0
                      ? "line.3.horizontal.decrease.circle.fill"
                      : "line.3.horizontal.decrease.circle")
            }
            .accessibilityLabel(model.filters.activeCount > 0
                                ? "Filters, \(model.filters.activeCount) active"
                                : "Filters")
        }
    }
}

/// Non-event destinations reachable from the feed. Separate from Event so
/// navigationDestination can discriminate by type.
enum FeedRoute: Hashable {
    case location
}
