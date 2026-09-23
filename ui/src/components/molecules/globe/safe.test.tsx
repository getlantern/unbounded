import React from 'react'
import {render, screen, waitFor} from '@testing-library/react'
import SafeGlobe from './safe'
import {Targets} from '../../../constants'
import {hasWebGL} from '../../../utils/capabilities'

jest.mock('../../../utils/capabilities', () => ({hasWebGL: jest.fn()}))

// The real placeholder needs AppContext; its look is irrelevant here.
jest.mock('./suspense', () => ({__esModule: true, default: () => <div data-testid={'globe-loading'} />}))

// Stands in for the real react-globe.gl component behind the lazy import.
const mockGlobe = jest.fn()
jest.mock('./index', () => ({__esModule: true, default: (props: object) => mockGlobe(props)}))

// Sits next to the globe the way the status toggle does in every layout.
const Widget = () => (
	<div>
		<SafeGlobe target={Targets.WEB} />
		<input type={'checkbox'} aria-label={'connect'} />
	</div>
)

beforeEach(() => {
	mockGlobe.mockReset()
	jest.spyOn(console, 'error').mockImplementation(() => {})
	jest.spyOn(console, 'warn').mockImplementation(() => {})
})
afterEach(() => jest.restoreAllMocks())

describe('SafeGlobe', () => {
	// The iOS Lockdown Mode failure: three.js cannot create a WebGL context and
	// throws while the globe mounts. With no boundary that unmounted the whole
	// widget, leaving an empty box with no toggle.
	test('keeps the rest of the widget when the globe throws', async () => {
		;(hasWebGL as jest.Mock).mockReturnValue(true)
		mockGlobe.mockImplementation(() => { throw new Error('Error creating WebGL context.') })
		render(<Widget />)
		await waitFor(() => expect(screen.queryByTestId('globe-loading')).not.toBeInTheDocument())
		expect(mockGlobe).toHaveBeenCalled()
		expect(screen.getByLabelText('connect')).toBeInTheDocument()
	})

	// Without WebGL the globe can only fail, so don't spend the download on it.
	// Suspending (loading placeholder) or rendering the globe (chunk already
	// cached by an earlier test) would each mean the lazy import was reached.
	test('renders nothing and never reaches the globe chunk without WebGL', () => {
		;(hasWebGL as jest.Mock).mockReturnValue(false)
		render(<Widget />)
		expect(screen.queryByTestId('globe-loading')).not.toBeInTheDocument()
		expect(mockGlobe).not.toHaveBeenCalled()
		expect(screen.getByLabelText('connect')).toBeInTheDocument()
	})

	test('renders the globe when WebGL works', async () => {
		;(hasWebGL as jest.Mock).mockReturnValue(true)
		mockGlobe.mockImplementation(() => <canvas data-testid={'globe'} />)
		render(<Widget />)
		expect(await screen.findByTestId('globe')).toBeInTheDocument()
		expect(screen.getByLabelText('connect')).toBeInTheDocument()
	})
})
