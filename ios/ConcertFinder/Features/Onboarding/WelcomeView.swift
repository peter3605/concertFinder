import SwiftUI
import UIKit

/// First run for an account with nothing to build a feed from: one that
/// signed in with Apple or email. See OnboardingModel for which steps appear
/// and why in this order.
struct WelcomeView: View {
    @Environment(OnboardingModel.self) private var onboarding

    var body: some View {
        NavigationStack {
            Group {
                switch onboarding.currentStep {
                case .name: NameStep()
                case .email: EmailStep()
                case .location: LocationStep()
                case .picks: PicksStep()
                case nil: ProgressView()
                }
            }
            .modifier(KeyboardVisibility())
            .navigationTitle(onboarding.currentStep?.title ?? "Welcome")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                // Title and counter together in the principal slot. The
                // counter alone in a leading slot was truncated to "Ste…".
                ToolbarItem(placement: .principal) {
                    VStack(spacing: 0) {
                        Text(onboarding.currentStep?.title ?? "Welcome")
                            .font(.headline)
                        if let progress = onboarding.progress {
                            Text("Step \(progress.index + 1) of \(progress.count)")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .accessibilityIdentifier("welcome-progress")
                        }
                    }
                }
            }
        }
    }
}

/// The button every step ends with, pinned under its form.
///
/// Hidden while the keyboard is up. Pinned to the safe area it rides on top
/// of the keyboard and covers the form rows behind it -- on the location
/// step, exactly the "Save city" button the person is reaching for.
private struct StepFooter<Extra: View>: View {
    @Environment(\.keyboardVisible) private var keyboardVisible
    let title: String
    let enabled: Bool
    let busy: Bool
    let action: () -> Void
    @ViewBuilder var extra: () -> Extra

    var body: some View {
        if !keyboardVisible {
            footer
        }
    }

    private var footer: some View {
        VStack(spacing: Metrics.tight) {
            Button(action: action) {
                if busy {
                    ProgressView().frame(maxWidth: .infinity)
                } else {
                    Text(title).frame(maxWidth: .infinity)
                }
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .disabled(!enabled || busy)
            .accessibilityIdentifier("welcome-continue")
            extra()
        }
        .padding(Metrics.gutter)
        .background(.bar)
    }
}

extension StepFooter where Extra == EmptyView {
    init(title: String, enabled: Bool, busy: Bool, action: @escaping () -> Void) {
        self.init(title: title, enabled: enabled, busy: busy, action: action, extra: { EmptyView() })
    }
}

// MARK: - Name

private struct NameStep: View {
    @Environment(AuthController.self) private var auth
    @Environment(OnboardingModel.self) private var onboarding
    @Environment(AppContainer.self) private var container
    @State private var name = ""
    @State private var busy = false
    @State private var error: String?

    private var trimmed: String { name.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        Form {
            Section {
                TextField("Your name", text: $name)
                    .textContentType(.name)
                    .submitLabel(.continue)
                    .onSubmit(save)
                    .accessibilityIdentifier("welcome-name")
            } footer: {
                Text("Shown in the app and in the emails we send you. Nobody else sees it.")
            }
            if let error {
                Section { Text(error).foregroundStyle(.red) }
            }
        }
        .safeAreaInset(edge: .bottom) {
            StepFooter(title: "Continue", enabled: !trimmed.isEmpty && trimmed.count <= 64, busy: busy, action: save)
        }
    }

    private func save() {
        guard !trimmed.isEmpty, !busy else { return }
        busy = true
        error = nil
        Task {
            defer { busy = false }
            do {
                let saved = try await container.api.setDisplayName(trimmed)
                if case .signedIn(var me) = auth.state {
                    me.displayName = saved
                    auth.updateProfile(me)
                }
                onboarding.advance()
            } catch let apiError as APIError {
                error = apiError.status == 400 ? "Use 1 to 64 characters, with no line breaks." : apiError.userMessage
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

// MARK: - Email

private struct EmailStep: View {
    @Environment(OnboardingModel.self) private var onboarding

    var body: some View {
        Form {
            Section {
                Text("A weekly email of new shows near you, and an alert when an artist you follow announces one. Confirming the address also lets you sign in with it.")
                    .foregroundStyle(.secondary)
            }
            EmailAddressSections(onVerified: { onboarding.advance() })
            Section {
                Button("Skip for now") { onboarding.advance() }
                    .accessibilityIdentifier("welcome-skip-email")
            } footer: {
                Text("You can add one later in Settings.")
            }
        }
    }
}

/// Add and confirm an address on a signed-in account: one mail, then the
/// code from it (or its link, which opens the app and confirms the same way).
/// Shared by onboarding and Settings.
struct EmailAddressSections: View {
    @Environment(AuthController.self) private var auth
    @Environment(AppContainer.self) private var container
    var onVerified: () -> Void = {}

    @State private var email = ""
    @State private var sentTo: String?
    @State private var code = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        if let sentTo {
            Section {
                TextField("6-digit code", text: $code)
                    .keyboardType(.numberPad)
                    .textContentType(.oneTimeCode)
                    .accessibilityIdentifier("verify-code")
                    .onChange(of: code) { _, value in
                        let cleaned = String(value.filter(\.isNumber).prefix(6))
                        if cleaned != value { code = cleaned }
                    }
                Button(busy ? "Confirming…" : "Confirm address", action: verify)
                    .disabled(code.count != 6 || busy)
                    .accessibilityIdentifier("verify-submit")
                Button("Use a different address") {
                    self.sentTo = nil
                    code = ""
                    error = nil
                }
            } header: {
                Text("Check your email")
            } footer: {
                Text("We sent a code to \(sentTo). You can also tap the link in that email on this phone.")
            }
        } else {
            Section {
                TextField("Email address", text: $email)
                    .keyboardType(.emailAddress)
                    .textContentType(.emailAddress)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .accessibilityIdentifier("verify-email")
                Button(busy ? "Sending…" : "Send confirmation", action: send)
                    .disabled(!email.contains("@") || busy)
                    .accessibilityIdentifier("verify-send")
            } header: {
                Text("Email address")
            }
        }
        if let error {
            Section { Text(error).foregroundStyle(.red) }
        }
    }

    private func send() {
        let address = email.trimmingCharacters(in: .whitespacesAndNewlines)
        busy = true
        error = nil
        Task {
            defer { busy = false }
            do {
                try await container.api.requestEmailVerification(email: address)
                sentTo = address
            } catch let apiError as APIError {
                error = apiError.userMessage
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    private func verify() {
        guard let sentTo else { return }
        busy = true
        error = nil
        Task {
            defer { busy = false }
            do {
                try await container.api.verifyEmail(email: sentTo, code: code)
                if let me = try? await container.api.me() {
                    auth.updateProfile(me)
                }
                onVerified()
            } catch let apiError as APIError {
                error = AuthController.signInError(apiError).userMessage
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

// MARK: - Location

private struct LocationStep: View {
    @Environment(OnboardingModel.self) private var onboarding
    @Environment(LocationModel.self) private var location

    private var chosen: Bool {
        guard let current = location.location else { return false }
        return !current.usesFallback
    }

    var body: some View {
        LocationView()
            .navigationTitle(OnboardingModel.Step.location.title)
            .safeAreaInset(edge: .bottom) {
                StepFooter(
                    title: onboarding.isLastStep ? "Show my concerts" : "Continue",
                    enabled: chosen,
                    busy: location.isSaving,
                    action: { onboarding.advance() }
                )
            }
    }
}

// MARK: - Picks

private struct PicksStep: View {
    @Environment(OnboardingModel.self) private var onboarding
    @Environment(PicksModel.self) private var picks

    var body: some View {
        Form {
            Section {
                Text("Pick artists, genres, or both. Your feed shows their upcoming concerts near you, and you can change these in Settings.")
                    .foregroundStyle(.secondary)
            }
            PicksEditorSections()
        }
        .task { if !picks.isLoaded { await picks.load() } }
        .safeAreaInset(edge: .bottom) {
            StepFooter(
                title: picks.total > 0 ? "Show my concerts" : "Pick at least one artist or genre",
                enabled: picks.total > 0,
                busy: false,
                action: { onboarding.advance() }
            )
        }
    }
}

// MARK: - Keyboard

private struct KeyboardVisibleKey: EnvironmentKey {
    static let defaultValue = false
}

extension EnvironmentValues {
    /// Whether the software keyboard is showing, for WelcomeView's footer.
    var keyboardVisible: Bool {
        get { self[KeyboardVisibleKey.self] }
        set { self[KeyboardVisibleKey.self] = newValue }
    }
}

/// Publishes the keyboard's visibility into the environment.
private struct KeyboardVisibility: ViewModifier {
    @State private var visible = false

    func body(content: Content) -> some View {
        content
            .environment(\.keyboardVisible, visible)
            .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in
                visible = true
            }
            .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillHideNotification)) { _ in
                visible = false
            }
    }
}
