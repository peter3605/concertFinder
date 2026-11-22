import AuthenticationServices
import SwiftUI

/// The signed-out screen: a Sign in tab and a Create an account tab, as on
/// the web (web/src/pages/login.tsx), so a person moving between clients
/// meets the same choices in the same order.
///
/// Apple and email lead because they are open to everyone. Spotify is capped
/// at five accounts for good (docs/public-path.md §7), so it sits below a
/// divider, labelled for invited accounts: an App Store reviewer who taps an
/// unlabelled Spotify button gets a 403 and reads it as a broken feature
/// (§6.1). On the create tab it appears only while site-info says a place is
/// free.
struct LoginView: View {
    @Environment(AuthController.self) private var auth

    enum Tab: Hashable {
        case signIn, create
    }

    @State private var tab: Tab = .signIn
    @State private var inviteCode = ""

    /// Whether the create tab still needs a code before anything can start.
    /// A rendering hint like the flags it reads: the server decides.
    private var needsCode: Bool {
        tab == .create && auth.inviteRequired
            && inviteCode.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// Only the create tab sends a code. A returning user never needs one,
    /// and sending a stale value typed on the other tab would be a lie about
    /// what they did.
    private var inviteToSend: String {
        tab == .create && auth.inviteRequired ? inviteCode : ""
    }

    var body: some View {
        ScrollView {
            VStack(spacing: Metrics.loose) {
                header
                    .padding(.top, Metrics.loose)

                if let pending = auth.pendingEmail {
                    EmailCodeStep(pending: pending)
                } else {
                    Picker("Account", selection: $tab) {
                        Text("Sign in").tag(Tab.signIn)
                        Text("Create an account").tag(Tab.create)
                    }
                    .pickerStyle(.segmented)

                    if tab == .create && auth.inviteRequired {
                        inviteField
                    }

                    if auth.appleSignInAvailable {
                        AppleButton(invite: inviteToSend)
                            .disabled(needsCode)
                            .opacity(needsCode ? 0.4 : 1)
                    }

                    EmailStartForm(
                        invite: inviteToSend,
                        submitLabel: tab == .signIn ? "Email me a sign-in link" : "Email me a link to create my account"
                    )
                    .disabled(needsCode)

                    if tab == .signIn || auth.spotifySignupsOpen {
                        spotifySection
                    }
                }

                if let error = auth.authError {
                    Text(error.userMessage)
                        .font(.footnote)
                        .foregroundStyle(.red)
                        .multilineTextAlignment(.center)
                        .accessibilityIdentifier("auth-error")
                }
            }
            .padding(Metrics.loose)
            .frame(maxWidth: 520)
            .frame(maxWidth: .infinity)
        }
        .scrollDismissesKeyboard(.interactively)
        .background(Color.screenBackground)
    }

    private var header: some View {
        VStack(spacing: Metrics.tight) {
            Image(systemName: "music.mic")
                // A relative style rather than a point size, so Dynamic Type
                // scales the icon with the text around it.
                .font(.system(.largeTitle, design: .default))
                .imageScale(.large)
                .foregroundStyle(Color.accentColor)
                .accessibilityHidden(true)
            Text("ConcertFinder")
                .font(.largeTitle.weight(.bold))
            Text("Upcoming shows near you by the artists you care about, in one feed.")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
    }

    private var inviteField: some View {
        VStack(alignment: .leading, spacing: Metrics.tight) {
            TextField("Invite code", text: $inviteCode)
                .textFieldStyle(.roundedBorder)
                .textInputAutocapitalization(.characters)
                .autocorrectionDisabled()
                .submitLabel(.done)
                .accessibilityLabel("Invite code")
            Text("New accounts need an invite while ConcertFinder is in early access. Already have an account? Use Sign in; accounts never need a code.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private var spotifySection: some View {
        VStack(spacing: Metrics.tight) {
            HStack {
                VStack { Divider() }
                Text("or").font(.caption).foregroundStyle(.secondary)
                VStack { Divider() }
            }
            SpotifyButton(invite: inviteToSend)
                .disabled(needsCode)
            Text(tab == .signIn
                 ? "For accounts that were invited to sign in with Spotify."
                 : "Only a few Spotify accounts can join, and only ones Spotify lets us add. We only read your library and never post, follow, or change anything.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            // The privacy position and the attribution stay with the button
            // they describe. LaunchUITests pins both: they are what a
            // reviewer reads about Spotify on the first screen.
            Text("We never see your Spotify password, and your listening history is never stored.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            SpotifyAttribution()
        }
    }
}

/// The system Sign in with Apple button.
///
/// The system control rather than a drawn one, because Apple's Human
/// Interface Guidelines require their own button's look: the logo, one of
/// their titles, black or white chosen to contrast with the background, and
/// no smaller than any other sign-in button on the screen. Black on light and
/// white on dark; the height matches the `.large` bordered buttons below it.
private struct AppleButton: View {
    @Environment(AuthController.self) private var auth
    @Environment(\.colorScheme) private var colorScheme
    let invite: String

    var body: some View {
        SignInWithAppleButton(.continue) { request in
            auth.prepare(request)
        } onCompletion: { result in
            Task { await auth.completeAppleSignIn(result, inviteCode: invite) }
        }
        .signInWithAppleButtonStyle(colorScheme == .dark ? .white : .black)
        .frame(height: 50)
        // The style is fixed at creation, so a scheme change needs a new view.
        .id(colorScheme)
        .accessibilityIdentifier("sign-in-apple")
    }
}

private struct SpotifyButton: View {
    @Environment(AuthController.self) private var auth
    @State private var isSigningIn = false
    let invite: String

    var body: some View {
        Button {
            isSigningIn = true
            Task {
                await auth.signIn(inviteCode: invite)
                isSigningIn = false
            }
        } label: {
            if isSigningIn {
                ProgressView().frame(maxWidth: .infinity)
            } else {
                Text("Continue with Spotify").frame(maxWidth: .infinity)
            }
        }
        .buttonStyle(.bordered)
        .controlSize(.large)
        .disabled(isSigningIn)
        .accessibilityIdentifier("sign-in-spotify")
    }
}

/// Address entry. The server answers 202 for every well-formed address, so
/// what follows is the same whether or not an account exists.
private struct EmailStartForm: View {
    @Environment(AuthController.self) private var auth
    @State private var email = ""
    @State private var isSending = false
    let invite: String
    let submitLabel: String

    private var canSend: Bool {
        let trimmed = email.trimmingCharacters(in: .whitespacesAndNewlines)
        return !isSending && trimmed.contains("@") && trimmed.contains(".")
    }

    var body: some View {
        VStack(spacing: Metrics.tight) {
            TextField("Email address", text: $email)
                .textFieldStyle(.roundedBorder)
                .keyboardType(.emailAddress)
                .textContentType(.emailAddress)
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.send)
                .onSubmit(send)
                .accessibilityIdentifier("email-address")
            Button(action: send) {
                if isSending {
                    ProgressView().frame(maxWidth: .infinity)
                } else {
                    Text(submitLabel).frame(maxWidth: .infinity)
                }
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .disabled(!canSend)
            .accessibilityIdentifier("email-send")
        }
    }

    private func send() {
        guard canSend else { return }
        isSending = true
        Task {
            await auth.startEmailSignIn(email: email, inviteCode: invite)
            isSending = false
        }
    }
}

/// After the mail is sent: tap the link on this phone, or type the code.
///
/// The code is the path for a mail read on another device, and for a link
/// that opened in Safari instead of here. Both finish with the verifier this
/// install generated, so a forwarded mail cannot sign anyone else in.
private struct EmailCodeStep: View {
    @Environment(AuthController.self) private var auth
    let pending: PendingEmailSignIn
    @State private var code = ""
    @State private var isSubmitting = false
    @State private var isResending = false
    @FocusState private var codeFocused: Bool

    private var digits: String { code.filter(\.isNumber) }

    var body: some View {
        VStack(spacing: Metrics.gutter) {
            VStack(spacing: Metrics.tight) {
                Image(systemName: "envelope.badge")
                    .font(.largeTitle)
                    .foregroundStyle(Color.accentColor)
                    .accessibilityHidden(true)
                Text("Check your email")
                    .font(.title2.weight(.semibold))
                Text("If \(pending.email) can sign in, we've sent it a link and a 6-digit code. Tap the link on this phone, or enter the code here. Both expire in 15 minutes.")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }

            TextField("6-digit code", text: $code)
                .textFieldStyle(.roundedBorder)
                .keyboardType(.numberPad)
                .textContentType(.oneTimeCode)
                .font(.title3.monospacedDigit())
                .multilineTextAlignment(.center)
                .focused($codeFocused)
                .accessibilityIdentifier("email-code")
                .onChange(of: code) { _, value in
                    let cleaned = String(value.filter(\.isNumber).prefix(6))
                    if cleaned != value { code = cleaned }
                }

            Button {
                isSubmitting = true
                Task {
                    await auth.submitEmailCode(digits)
                    isSubmitting = false
                }
            } label: {
                if isSubmitting {
                    ProgressView().frame(maxWidth: .infinity)
                } else {
                    Text("Sign in").frame(maxWidth: .infinity)
                }
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .disabled(digits.count != 6 || isSubmitting)
            .accessibilityIdentifier("email-code-submit")

            HStack {
                Button("Use a different address") { auth.cancelEmailSignIn() }
                Spacer()
                Button(isResending ? "Sending…" : "Send again") {
                    isResending = true
                    Task {
                        // A resend replaces the pending verifier; the older
                        // mail's code is still the server's to honour or not.
                        await auth.startEmailSignIn(email: pending.email, inviteCode: pending.invite ?? "")
                        isResending = false
                    }
                }
                .disabled(isResending)
            }
            .font(.footnote)
        }
        .onAppear { codeFocused = true }
    }
}
