// Demo pipeline showing a pending input with parameters
// (choice + boolean). Used to manually validate the
// "v0.1 sees parameters but can't submit them" gap.
pipeline {
    agent any
    stages {
        stage('Before') {
            steps {
                echo 'preparing deploy'
            }
        }
        stage('Approval') {
            steps {
                input(
                    message: 'Deploy to which environment?',
                    id: 'deploy',
                    ok: 'Deploy',
                    parameters: [
                        choice(name: 'ENV', choices: ['staging', 'prod'], description: 'Target environment'),
                        booleanParam(name: 'DRY_RUN', defaultValue: true, description: 'Skip side effects')
                    ]
                )
            }
        }
        stage('After') {
            steps {
                echo 'deploy decision recorded'
            }
        }
    }
}
