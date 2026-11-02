#pragma once
// Native vector tests only. Firmware uses ESP-IDF's PSA configuration.
#define TF_PSA_CRYPTO_CONFIG_VERSION 0x01000000
#define PSA_WANT_ALG_GCM 1
#define PSA_WANT_ALG_SHA_256 1
#define PSA_WANT_ALG_HMAC 1
#define PSA_WANT_KEY_TYPE_AES 1
#define PSA_WANT_KEY_TYPE_HMAC 1
#define MBEDTLS_PSA_CRYPTO_C
#define MBEDTLS_CTR_DRBG_C
#define MBEDTLS_PSA_BUILTIN_GET_ENTROPY
#define MBEDTLS_PLATFORM_C
#if defined(__unix__) || defined(__APPLE__)
#define MBEDTLS_THREADING_C
#define MBEDTLS_THREADING_PTHREAD
#endif
