module controlplane

go 1.25.0

require (
	github.com/go-chi/chi/v5 v5.3.1
	github.com/go-chi/httprate v0.16.0
	github.com/go-webauthn/webauthn v0.17.4
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/golang-migrate/migrate/v4 v4.19.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/justinas/nosurf v1.2.0
	github.com/rs/cors v1.11.1
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/stripe/stripe-go/v86 v86.2.0
	golang.org/x/crypto v0.54.0
	golang.zx2c4.com/wireguard/wgctrl v0.0.0-20241231184526-a9ab2273dd10
)

// golang-migrate v4 requires adal v0.9.16, which upstream has retracted; force the fixed v0.9.24.
replace github.com/Azure/go-autorest/autorest/adal v0.9.16 => github.com/Azure/go-autorest/autorest/adal v0.9.24

require (
	github.com/fxamacker/cbor/v2 v2.9.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.2.6 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/jackc/pgerrcode v0.0.0-20220416144525-469b46aa5efa // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/zeebo/xxh3 v1.0.2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)
