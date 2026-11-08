# Changelog

All notable changes to this project will be documented in this file. See [standard-version](https://github.com/conventional-changelog/standard-version) for commit guidelines.

### [0.2.7](https://github.com/dsswift/redgreen/compare/v0.2.6...v0.2.7) (2026-10-16)


### Features

* detect readme, dockerfile, tests per service ([b1cfa92](https://github.com/dsswift/redgreen/commit/b1cfa92c8ea8ff10c75443489b9106e604e028d4))
* read gitlab repository facts ([8be59ab](https://github.com/dsswift/redgreen/commit/8be59ab2c4ce587010871e064dc76119c7cbd6ca))

### [0.2.6](https://github.com/dsswift/redgreen/compare/v0.2.5...v0.2.6) (2026-10-14)


### Features

* publish the image to the github registry ([dfe5a82](https://github.com/dsswift/redgreen/commit/dfe5a824067d0f9d279c27faaf066d990a62fcaa))
* stamp services with who last changed them ([bb87890](https://github.com/dsswift/redgreen/commit/bb878904a1ab2b3f0817d3017afab803bc10c17b))

### [0.2.5](https://github.com/dsswift/redgreen/compare/v0.2.4...v0.2.5) (2026-10-12)


### Features

* stamp services with their folder's last change ([9a1abf8](https://github.com/dsswift/redgreen/commit/9a1abf801d48e1832b35f6f96364b1b3a571a4e9))

### [0.2.4](https://github.com/dsswift/redgreen/compare/v0.2.3...v0.2.4) (2026-10-08)


### Bug Fixes

* sign in again when the sign-in has ended ([a484f06](https://github.com/dsswift/redgreen/commit/a484f068265cf6549f271b7ce440c44bdb0e0b71))

### [0.2.3](https://github.com/dsswift/redgreen/compare/v0.2.2...v0.2.3) (2026-10-06)


### Features

* push the services found in each repo ([f440c77](https://github.com/dsswift/redgreen/commit/f440c7701ab122e5a11b9f4c58c56e2ae99ede17))

### [0.2.2](https://github.com/dsswift/redgreen/compare/v0.2.1...v0.2.2) (2026-10-05)


### Bug Fixes

* read a gitlab project with features off ([d109845](https://github.com/dsswift/redgreen/commit/d109845f8a16e8a401e92eaefe644c5db6641dfd))

### [0.2.1](https://github.com/dsswift/redgreen/compare/v0.2.0...v0.2.1) (2026-10-05)


### Features

* add azure devops provider ([1bf5b28](https://github.com/dsswift/redgreen/commit/1bf5b282af51ac59b79615b83d8fac19961e285a))
* one language list and open fields ([dafe517](https://github.com/dsswift/redgreen/commit/dafe5174f0f73ab5ec834e686569882f91c35749))
* push forge deployments to orrery ([185abff](https://github.com/dsswift/redgreen/commit/185abff406076d4b4a343b502ade925caf96625e))

## [1.3.0](https://github.com/dsswift/redgreen/compare/v1.2.0...v1.3.0) (2026-11-08)

### Features

* read ci jobs from gitlab and azure devops ([0e34f5e](https://github.com/dsswift/redgreen/commit/0e34f5e05f39dbf95ff4ec5661bcd4eeb219d0c9))

## [1.2.0](https://github.com/dsswift/redgreen/compare/v1.1.1...v1.2.0) (2026-11-08)

### Features

* measure ci jobs on every kind of runner ([886b618](https://github.com/dsswift/redgreen/commit/886b618091cac65525d4af47467998bf6d2394cf))

## [1.1.1](https://github.com/dsswift/redgreen/compare/v1.1.0...v1.1.1) (2026-11-08)

### Bug Fixes

* give each terraform root its own service ([0a2b857](https://github.com/dsswift/redgreen/commit/0a2b857d0b5c15e67957c612229196cecdcf4a97))

## [1.1.0](https://github.com/dsswift/redgreen/compare/v1.0.1...v1.1.0) (2026-11-07)

### Features

* carry gitlab and azure devops from an old store ([664141f](https://github.com/dsswift/redgreen/commit/664141fda3ee95886f3727e0babf80566742cb53))
* keep the delivery measures' history ([f70a049](https://github.com/dsswift/redgreen/commit/f70a049336a42c19d87abba1402ce0930ea7fee2))

## [1.0.1](https://github.com/dsswift/redgreen/compare/v1.0.0...v1.0.1) (2026-11-07)

### Bug Fixes

* quiet the pipelines of a muted repository ([6e0b695](https://github.com/dsswift/redgreen/commit/6e0b695b3c5d1244efbb09888013250f1fbfd646))

## [1.0.0](https://github.com/dsswift/redgreen/compare/v0.2.19...v1.0.0) (2026-11-07)

### ⚠ BREAKING CHANGES

* rebuild redgreen as a headless go service ([350baa1](https://github.com/dsswift/redgreen/commit/350baa104dd64195e34e1e0d12a94b1b15b0083a))

### Features

* rebuild redgreen as a headless go service ([350baa1](https://github.com/dsswift/redgreen/commit/350baa104dd64195e34e1e0d12a94b1b15b0083a))
* check calculations with orrery's compiler ([f594561](https://github.com/dsswift/redgreen/commit/f5945610557ef94aacdf62ffda840d48e42021d3))

### Bug Fixes

* keep a repository with no pipelines quiet ([47b3ef6](https://github.com/dsswift/redgreen/commit/47b3ef6fab15c6c3bb659d8832e82b9d73ace24c))

## [0.2.19](https://github.com/dsswift/redgreen/compare/v0.2.18...v0.2.19) (2026-11-04)

### Bug Fixes

* call orrery under /v1 ([2a6665f](https://github.com/dsswift/redgreen/commit/2a6665ffca5e747db987c33cdf01425ab1de3c22))

## [0.2.18](https://github.com/dsswift/redgreen/compare/v0.2.17...v0.2.18) (2026-11-04)

### Features

* carry each repo's own logo to orrery ([2e8a21f](https://github.com/dsswift/redgreen/commit/2e8a21fff934cc5262da2d1597239610555b7ea1))

## [0.2.17](https://github.com/dsswift/redgreen/compare/v0.2.16...v0.2.17) (2026-11-02)

### Bug Fixes

* sign the image and attest its sbom ([cc4999e](https://github.com/dsswift/redgreen/commit/cc4999ea6283088a64b12c3027d21da35d8cf101))

## [0.2.16](https://github.com/dsswift/redgreen/compare/v0.2.15...v0.2.16) (2026-11-02)

### Features

* write orrery under the standard blueprint ids ([2d67912](https://github.com/dsswift/redgreen/commit/2d67912f831cd0bc2cde82302ea14488914cc778))

## [0.2.15](https://github.com/dsswift/redgreen/compare/v0.2.14...v0.2.15) (2026-11-01)

### Features

* describe redgreen itself to orrery ([3d98650](https://github.com/dsswift/redgreen/commit/3d986508b5d4ecf1ca1775f50761ba6063b3aea9))

## [0.2.14](https://github.com/dsswift/redgreen/compare/v0.2.13...v0.2.14) (2026-11-01)

### Features

* count incidents from dispatch ([fde43ed](https://github.com/dsswift/redgreen/commit/fde43edefaae1407d9691867d188c9b3729dea31))

## [0.2.13](https://github.com/dsswift/redgreen/compare/v0.2.12...v0.2.13) (2026-10-31)

### Features

* add outcomes, the four dora measures ([4480d2f](https://github.com/dsswift/redgreen/commit/4480d2fc9b500745ab3873987bd0617ba1be6e0c))

## [0.2.12](https://github.com/dsswift/redgreen/compare/v0.2.11...v0.2.12) (2026-10-27)

### Bug Fixes

* count a .invalid address as a bot ([4c2ba31](https://github.com/dsswift/redgreen/commit/4c2ba31dde0f392bcc70eb8602e9f515ebd745ef))

## [0.2.11](https://github.com/dsswift/redgreen/compare/v0.2.10...v0.2.11) (2026-10-27)

### Features

* push the commit author address beside the name ([77571c0](https://github.com/dsswift/redgreen/commit/77571c0127f35af156ee10e53cc8040ddb251340))

## [0.2.10](https://github.com/dsswift/redgreen/compare/v0.2.9...v0.2.10) (2026-10-23)

### Bug Fixes

* reset the http cache by copy, not drop ([2f38c55](https://github.com/dsswift/redgreen/commit/2f38c551168c438afd2c185bfe8fdd0c37c553e5))

## [0.2.9](https://github.com/dsswift/redgreen/compare/v0.2.8...v0.2.9) (2026-10-23)

### Bug Fixes

* keep the http cache small and indexed ([76bbb6c](https://github.com/dsswift/redgreen/commit/76bbb6ce7b6347524ac799a2354ca01d3cb3ed83))

## [0.2.8](https://github.com/dsswift/redgreen/compare/v0.2.7...v0.2.8) (2026-10-22)

### Features

* push last human change beside last change ([0e04301](https://github.com/dsswift/redgreen/commit/0e043014c453826f8357a4ad683cb8ac73fd36d7))

## [0.2.0](https://github.com/dsswift/redgreen/compare/v0.1.2...v0.2.0) (2026-10-01)


### Features

* push every repo to an orrery hub as a repository, with branch rules, last commit, and repo facts; the repository blueprint is offered to the hub first

### [0.1.2](https://github.com/dsswift/redgreen/compare/v0.1.1...v0.1.2) (2026-09-30)


### Features

* add gitlab provider with oauth setup ([ee368f0](https://github.com/dsswift/redgreen/commit/ee368f01245fa2f40544c0e12f26158c7bc214ac))
