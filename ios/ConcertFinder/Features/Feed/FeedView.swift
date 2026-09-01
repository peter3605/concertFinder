import SwiftUI

struct FeedView: View {
    @Environment(FeedModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @State private var showingFilters = false

    var body: some View {
        @Bindable var model = model

        NavigationStack(path: $model.path) {
            Group {
                // A genuine cold start is minutes of work, not a spinner's
                // worth. See FirstRunView and plan §5.5.
                if model.isFirstRun {
                    FirstRunView()
                } else if model.isLoading && model.events.isEmpty {
                    ProgressView("Finding concerts near you…")
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if model.isEmpty {
                    emptyState
                } else {
                    list
                }
            }
            // On the Group, not on `list`. Attached to the list they covered
            // only the branch that renders it, so FirstRunView's
            // NavigationLink(value:) had no destination and its cards were
            // dead — a tap did nothing but log a purple runtime warning. The
            // path-driven push from a notification needs the same coverage.
            .navigationDestination(for: Event.self) { event in
                EventDetailView(event: event)
            }
            .navigationDestination(for: FeedRoute.self) { route in
                switch route {
                case .location: LocationView()
                }
            }
            .background(Color.screenBackground)
            .navigationTitle("Concerts")
            .toolbar { toolbar }
            // A pull re-reads the snapshot, which is free. The rescan that
            // spends the user's daily Ticketmaster allowance is a deliberate
            // tap in the toolbar — a gesture people make by accident should
            // not cost quota, and the 429 it earned was never shown.
            .refreshable { await model.load() }
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
    }

    @ViewBuilder
    private var banners: some View {
        // Four models set `error` and this was the screen that rendered none
        // of it: a 500 or a decode failure left an empty list under "Nothing
        // coming up near you yet", which blames the user's city for our
        // outage. Suppressed only when the offline banner below already says
        // it, so the two do not stack.
        if let error = model.error, !model.isShowingCachedData {
            InfoBanner(kind: .error(error.userMessage))
        }
        if let refusal = model.rescanRefusal {
            InfoBanner(kind: .throttled(reason: refusal.reason, until: refusal.until))
        }
        if model.missingDeepLinkEvent {
            InfoBanner(kind: .missingEvent)
        }
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
        if model.isUsingFallbackLocation {
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

    /// An empty list has two very different causes and they were told the same
    /// story. "Nothing coming up near you yet" over a failed request is a lie
    /// about the user's city.
    private var emptyTitle: String {
        guard let error = model.error else { return "No concerts yet" }
        // A refused rescan did not fail to load anything — the list is empty
        // for whatever reason it already was.
        if case .throttled = error { return "No concerts yet" }
        return "We couldn't load your concerts"
    }

    private var hasLoadFailure: Bool {
        guard let error = model.error else { return false }
        if case .throttled = error { return false }
        return true
    }

    private var emptyState: some View {
        ContentUnavailableView {
            Label(emptyTitle, systemImage: hasLoadFailure ? "exclamationmark.triangle" : "music.mic")
        } description: {
            if let error = model.error {
                Text(error.userMessage)
            } else if !model.complete {
                Text("We couldn't check every artist in your profile. Search again, or widen your search radius.")
            } else if model.filters.activeCount > 0 {
                Text("No shows match your filters. Try clearing them.")
            } else {
                Text("Nothing coming up near you yet. We check daily and will notify you when something lands.")
            }
        } actions: {
            // ContentUnavailableView does not scroll, so "pull to refresh" was
            // advice the user could not follow from the only screen that
            // offered it. A button can be tapped.
            if hasLoadFailure {
                Button("Try again") { Task { await model.load() } }
            } else if !model.complete {
                Button("Search again") { Task { await model.requestRescan() } }
                    .disabled(!model.canRescan)
            }
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
                Task { await model.requestRescan() }
            } label: {
                if model.isRescanning {
                    ProgressView().controlSize(.small)
                } else {
                    Label("Search again", systemImage: "arrow.clockwise")
                }
            }
            .disabled(!model.canRescan)
            .accessibilityLabel("Search for new shows")
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
