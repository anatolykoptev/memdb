# Changelog

## [1.19.1](https://github.com/anatolykoptev/go-stealth/compare/v1.19.0...v1.19.1) (2026-07-18)


### Fixed

* **oxbrowser:** check json.Marshal errors in Solve/FetchSmart/Analyze ([#26](https://github.com/anatolykoptev/go-stealth/issues/26)) ([c5c516d](https://github.com/anatolykoptev/go-stealth/commit/c5c516dab8d719d158ccf1b9296f433e5a8a9f44))
* **proxypool:** guard Webshare.Next() against empty pool panic ([#25](https://github.com/anatolykoptev/go-stealth/issues/25)) ([e67cca4](https://github.com/anatolykoptev/go-stealth/commit/e67cca4d3382fda02fc667f922447eae835ff6b8))
* **roundtripper:** preserve multi-value Set-Cookie headers correctly ([#28](https://github.com/anatolykoptev/go-stealth/issues/28)) ([9610d94](https://github.com/anatolykoptev/go-stealth/commit/9610d945e477fc19a274c6febe7d918386e1be9d))


### Changed

* consolidate extractDomain into shared internal/uri.ExtractHost ([#27](https://github.com/anatolykoptev/go-stealth/issues/27)) ([d722ab7](https://github.com/anatolykoptev/go-stealth/commit/d722ab7108f2b4002b8e0b16b42b7b1b9b720b1e))

## [1.19.0](https://github.com/anatolykoptev/go-stealth/compare/v1.18.1...v1.19.0) (2026-07-18)


### Added

* **backend:** std backend honors InsecureSkipVerify for opt-in parity ([#11](https://github.com/anatolykoptev/go-stealth/issues/11)) ([fe977f7](https://github.com/anatolykoptev/go-stealth/commit/fe977f7116222511d107030d82a8c36034e549af))


### Fixed

* **security:** secure-by-default TLS verification with opt-in skip-verify ([#7](https://github.com/anatolykoptev/go-stealth/issues/7)) ([368c09c](https://github.com/anatolykoptev/go-stealth/commit/368c09ce0c42510b9e44ede878d8a67227bb98e8))


### Documentation

* v2.0.0 breaking change — secure-by-default TLS verification ([#13](https://github.com/anatolykoptev/go-stealth/issues/13)) ([7deb852](https://github.com/anatolykoptev/go-stealth/commit/7deb852018eb309dc39da378a2c3e42a2baa6540))
