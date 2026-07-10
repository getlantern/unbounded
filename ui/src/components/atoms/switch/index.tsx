import React, {ChangeEvent, useContext} from 'react'
import {Container, Input, Label, LoadingSpinner, Slider} from './styles'
import {AppContext} from '../../../context'
import {useEmitterState} from '../../../hooks/useStateEmitter'
import {tutorialOnEmitter} from '../tutorial'

interface Props {
	onToggle: (e: boolean) => void
	checked: boolean
	disabled: boolean
	loading: boolean
	small?: boolean // force the compact size regardless of menu/collapse settings
}

const Switch = ({onToggle, checked, disabled, loading, small}: Props) => {
	const tutorialOn = useEmitterState(tutorialOnEmitter);
	const {theme, menu, collapse} = useContext(AppContext).settings
	const isLarge = !small && !menu && !collapse

	return (
		<Container>
			<Label
				style={isLarge ? {height: 30, width: 52} : {}}
				$animate={isLarge && tutorialOn}
			>
				<Input
					type={'checkbox'}
					onChange={(e: ChangeEvent<HTMLInputElement>) =>
						onToggle(e.currentTarget.checked)
					}
					checked={checked}
					aria-label={'connect'}
					disabled={disabled}
					name={'browsers-unbounded-connect'}
				/>
				<Slider
					checked={checked}
					disabled={disabled}
					theme={theme}
					$loading={loading}
					$isLarge={isLarge}
				/>
				{
					loading && (
						<LoadingSpinner
							$isLarge={isLarge}
						/>
					)
				}
			</Label>
		</Container>
	)
}

export default Switch